package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/app"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

// The harness run end to end against an in-process server and a small
// synthetic file in the BGL format, so changes to it are caught without
// needing the 743 MB dataset.
func TestHarnessRunsEndToEnd(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		level, label := "INFO", "-"
		if i%10 == 0 {
			level, label = "FATAL", "KERNDTLB"
		}
		// The first third of the lines don't contain the word "error", so some
		// segments can be ruled out by a search for it. Without that, a
		// harness that mistook a word search for a full scan would still pass.
		word := "error"
		if i < 1000 {
			word = "notice"
		}
		fmt.Fprintf(&sb, "%s 1117838570 2005.06.03 R%02d-M1-N0-C:J12-U11 2005-06-03-%02d.%02d.%02d.%06d R%02d-M1-N0-C:J12-U11 RAS KERNEL %s %s number %d occurred\n",
			label, i%32, 10+i/3600, (i/60)%60, i%60, i, i%32, level, word, i)
	}
	file := filepath.Join(t.TempDir(), "bgl.log")
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	mem := storagetest.NewMem()
	srv, err := app.Start(context.Background(), app.Config{
		ListenAddr: "127.0.0.1:0", ManifestPath: filepath.Join(t.TempDir(), "m.db"), Storage: mem,
		BufferBytes: 20 << 10, BufferAge: 50 * time.Millisecond, MaxRecvMsgBytes: 4 << 20,
		CompactEnabled: false, // keeps the segments separate, so bloom filters have something to skip
		ShutdownGrace:  time.Second, OrphanGrace: time.Hour, LogLevel: "error",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	conn, err := grpc.NewClient(srv.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	c := config{Dataset: file, Streams: 3, Batch: 100, Iterations: 5, HeavyIters: 2, Settle: 400 * time.Millisecond,
		SealWait: 200 * time.Millisecond, VerifySample: 50, Seed: 1}

	ing, err := runIngest(ctx, stratav1.NewIngestServiceClient(conn), c)
	if err != nil {
		t.Fatal(err)
	}
	if ing.Lines != 3000 || ing.LinesPerSec <= 0 || ing.BatchP99Ms < ing.BatchP50Ms {
		t.Fatalf("ingest result: %+v", ing)
	}
	q := stratav1.NewQueryServiceClient(conn)
	settle, err := waitForSettle(ctx, q, c, ing)
	if err != nil {
		t.Fatal(err)
	}
	if settle.SegmentsSettled < 1 || settle.SegmentsSettled > settle.SegmentsAfterIngest {
		t.Fatalf("settle: %+v", settle)
	}
	// The reported stored size must equal what is really in the bucket. (An
	// earlier version measured it with a word search, which skips segments by
	// bloom filter and silently undercounted.)
	var actual int64
	keys, _ := mem.List(ctx, "segments/")
	for _, k := range keys {
		r, err := mem.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, r)
		r.Close()
		actual += n
	}
	if settle.StoredBytes != actual || actual == 0 {
		t.Fatalf("harness reports %d stored bytes, the bucket holds %d", settle.StoredBytes, actual)
	}
	samples, err := sampleLines(c)
	if err != nil || len(samples) != 50 {
		t.Fatalf("sampling: %d %v", len(samples), err)
	}
	// Same seed, same sample: reproducibility is the point of the seed.
	again, _ := sampleLines(c)
	for i := range samples {
		if samples[i].raw != again[i].raw {
			t.Fatal("sampling is not reproducible")
		}
	}
	v := runVerify(ctx, q, samples)
	if v.Found != v.Sampled {
		t.Fatalf("verification found %d of %d: the harness would not notice lost data", v.Found, v.Sampled)
	}
	results := runQueries(ctx, q, c, samples)
	if len(results) != 7 {
		t.Fatalf("%d query cases", len(results))
	}
	for _, r := range results {
		if r.Errors != 0 || r.P50Ms <= 0 || r.P99Ms < r.P50Ms || r.Considered == 0 {
			t.Errorf("query result: %+v", r)
		}
	}

	md := renderMarkdown(&report{Env: gatherEnv(), Config: c, Ingest: ing, Settle: settle, Verify: v, Queries: results})
	for _, want := range []string{"# Strata benchmark results", "Ingest throughput", "Query latency", "Correctness check", "worst case"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// If data is missing the verification must say so; otherwise the benchmark
// could report fast numbers over a store that lost lines.
func TestVerifyNoticesMissingData(t *testing.T) {
	line := "- 1117838570 2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.50.363779 R02-M1-N0-C:J12-U11 RAS KERNEL INFO never ingested"
	srv, err := app.Start(context.Background(), app.Config{
		ListenAddr: "127.0.0.1:0", ManifestPath: filepath.Join(t.TempDir(), "m.db"), Storage: storagetest.NewMem(),
		BufferBytes: 1 << 20, BufferAge: time.Second, MaxRecvMsgBytes: 4 << 20, ShutdownGrace: time.Second,
		OrphanGrace: time.Hour, LogLevel: "error", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	conn, _ := grpc.NewClient(srv.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()

	file := filepath.Join(t.TempDir(), "one.log")
	os.WriteFile(file, []byte(line+"\n"), 0o644)
	samples, err := sampleLines(config{Dataset: file, VerifySample: 5, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v := runVerify(context.Background(), stratav1.NewQueryServiceClient(conn), samples); v.Found != 0 || v.Sampled != 1 {
		t.Fatalf("verification claimed to find data that was never ingested: %+v", v)
	}
}
