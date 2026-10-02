package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/query"
	"github.com/dharmikchandel/strata/internal/storage"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var ctx = context.Background()

func testConfig(t *testing.T, store storage.Storage) Config {
	t.Helper()
	return Config{
		ListenAddr:      "127.0.0.1:0",
		ManifestPath:    filepath.Join(t.TempDir(), "manifest.db"),
		Storage:         store,
		BufferBytes:     1 << 30, // nothing seals by size or age unless a test asks
		BufferAge:       time.Hour,
		MaxRecvMsgBytes: 4 << 20,
		ShutdownGrace:   2 * time.Second,
		OrphanGrace:     time.Hour,
		LogLevel:        "error",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func dial(t *testing.T, a *App) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(a.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ingestLines sends n uniquely named lines in batches of 50 over one stream
// and waits for every acknowledgement.
func ingestLines(t *testing.T, a *App, prefix string, n int) {
	t.Helper()
	st, err := stratav1.NewIngestServiceClient(dial(t, a)).Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for sent := 0; sent < n; {
		var batch []*stratav1.LogEntry
		for ; len(batch) < 50 && sent < n; sent++ {
			batch = append(batch, &stratav1.LogEntry{TimestampUnixNano: int64(1000 + sent), Message: fmt.Sprintf("%s-%05d common", prefix, sent)})
		}
		if err := st.Send(&stratav1.IngestRequest{BatchId: uint64(sent), Entries: batch}); err != nil {
			t.Fatal(err)
		}
		resp, err := st.Recv()
		if err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
			t.Fatalf("batch not accepted: %+v %v", resp, err)
		}
	}
	st.CloseSend()
}

func shutdown(t *testing.T, a *App) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := a.Shutdown(c); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// readBack reopens the manifest from disk, as a restarted process would, and
// returns every stored message.
func readBack(t *testing.T, cfg Config) (msgs []string, segments int) {
	t.Helper()
	m, err := manifest.Open(cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	res, err := query.New(m, cfg.Storage, 4).Search(ctx, query.Query{Limit: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		msgs = append(msgs, h.Message)
	}
	return msgs, res.Metrics.SegmentsConsidered
}

func assertExactlyOnce(t *testing.T, msgs []string, want int) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m] {
			t.Fatalf("%q stored twice", m)
		}
		seen[m] = true
	}
	if len(msgs) != want {
		t.Fatalf("found %d entries, want %d", len(msgs), want)
	}
}

// ---------------------------------------------------------------------------

// The whole point of the shutdown order: logs that were acknowledged but not
// yet sealed (nothing here seals by size or age) must be written out when the
// server is stopped.
func TestShutdownFlushesAcknowledgedLogs(t *testing.T) {
	cfg := testConfig(t, storagetest.NewMem())
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a, "flush", 500)
	if a.buffer.Pending() != 500 {
		t.Fatalf("expected the lines to still be buffered, pending=%d", a.buffer.Pending())
	}
	shutdown(t, a)

	msgs, segs := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 500)
	if segs < 1 {
		t.Fatal("no segment recorded")
	}
}

func TestRestartKeepsDataAndAcceptsMore(t *testing.T) {
	cfg := testConfig(t, storagetest.NewMem())
	a1, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a1, "first", 100)
	shutdown(t, a1)

	a2, err := Start(ctx, cfg) // same manifest file, same storage
	if err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	ingestLines(t, a2, "second", 100)
	shutdown(t, a2)

	msgs, _ := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 200)
}

func TestShutdownIsIdempotent(t *testing.T) {
	a, err := Start(ctx, testConfig(t, storagetest.NewMem()))
	if err != nil {
		t.Fatal(err)
	}
	shutdown(t, a)
	shutdown(t, a)
}

func TestHealthCheckReportsServing(t *testing.T) {
	a, err := Start(ctx, testConfig(t, storagetest.NewMem()))
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, a)
	resp, err := healthpb.NewHealthClient(dial(t, a)).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("%v %v", resp, err)
	}
}

// A client keeps its stream open and never finishes. Shutdown must mark the
// server NOT_SERVING right away, give the stream ShutdownGrace to finish, then
// cut it off, and must still write out everything that was acknowledged.
func TestShutdownDrainsThenCutsOffStuckStreams(t *testing.T) {
	cfg := testConfig(t, storagetest.NewMem())
	cfg.ShutdownGrace = 400 * time.Millisecond
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn := dial(t, a)
	st, err := stratav1.NewIngestServiceClient(conn).Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.Send(&stratav1.IngestRequest{BatchId: 1, Entries: []*stratav1.LogEntry{{Message: "acked before shutdown"}}})
	if resp, err := st.Recv(); err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("%v %v", resp, err)
	}
	// ...and the stream stays open.

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		done <- a.Shutdown(c)
	}()

	// During the drain the health check flips to NOT_SERVING (the existing
	// connection still works, which is how a load balancer would see it).
	hc := healthpb.NewHealthClient(conn)
	deadline := time.Now().Add(300 * time.Millisecond)
	notServing := false
	for time.Now().Before(deadline) && !notServing {
		if r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{}); err == nil && r.Status == healthpb.HealthCheckResponse_NOT_SERVING {
			notServing = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !notServing {
		t.Error("health check never reported NOT_SERVING during shutdown")
	}

	// While draining, an in-flight stream must still be served: the buffer is
	// only closed after the gRPC server has stopped. (Closing it first would
	// make this batch fail with Unavailable.)
	st.Send(&stratav1.IngestRequest{BatchId: 2, Entries: []*stratav1.LogEntry{{Message: "accepted during drain"}}})
	if resp, err := st.Recv(); err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("batch sent during the drain was refused: %+v %v", resp, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if took := time.Since(start); took < 350*time.Millisecond || took > 5*time.Second {
		t.Fatalf("shutdown took %v: expected to wait about the 400ms grace, then stop", took)
	}
	msgs, _ := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 2)
}

// If storage is down at shutdown, the final flush can't succeed. Shutdown must
// not hang, and must say how much was lost rather than pretend all is well.
func TestShutdownWithBrokenStorageReportsLoss(t *testing.T) {
	mem := storagetest.NewMem()
	cfg := testConfig(t, mem)
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a, "doomed", 10)
	mem.FailNextPuts(1 << 20)

	start := time.Now()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = a.Shutdown(c)
	if err == nil || !strings.Contains(err.Error(), "10 buffered entries lost") {
		t.Fatalf("want an error reporting 10 lost entries, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("shutdown hung on broken storage")
	}
}

// Compaction runs inside the server: many small segments get merged, and
// queries over the result are unaffected.
func TestCompactionRunsInTheBackground(t *testing.T) {
	cfg := testConfig(t, storagetest.NewMem())
	cfg.BufferBytes = 2000 // seal often
	cfg.CompactEnabled = true
	cfg.CompactInterval = 10 * time.Millisecond
	cfg.CompactSmallBytes = 1 << 20
	cfg.CompactTargetBytes = 8 << 20
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a, "bg", 1500)

	// Wait for the compactor to bring the segment count down.
	var active int
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, err := a.manifest.List(ctx, manifest.StatusActive)
		if err != nil {
			t.Fatal(err)
		}
		active = len(rows)
		if active <= 3 && active > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("compaction never reduced the segment count: %d active", active)
		}
		time.Sleep(20 * time.Millisecond)
	}
	shutdown(t, a)
	msgs, _ := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 1500)
}

// ---- startup failures -----------------------------------------------------

func TestStartFailsFastAndCleansUp(t *testing.T) {
	t.Run("invalid config", func(t *testing.T) {
		cfg := testConfig(t, storagetest.NewMem())
		cfg.BufferBytes = 0
		if _, err := Start(ctx, cfg); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("port already in use", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		cfg := testConfig(t, storagetest.NewMem())
		cfg.ListenAddr = busy.Addr().String()
		_, err = Start(ctx, cfg)
		if err == nil || !strings.Contains(err.Error(), "listen") {
			t.Fatalf("want a listen error, got %v", err)
		}
		// Nothing was left open: the manifest can be opened again, and a
		// server can start on a free port with the same manifest file.
		cfg.ListenAddr = "127.0.0.1:0"
		a, err := Start(ctx, cfg)
		if err != nil {
			t.Fatalf("retry after failed start: %v", err)
		}
		shutdown(t, a)
	})

	t.Run("object store unreachable", func(t *testing.T) {
		cfg := testConfig(t, nil)
		cfg.S3 = storage.S3Config{Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true, OpTimeout: 500 * time.Millisecond}
		start := time.Now()
		_, err := Start(ctx, cfg)
		if err == nil || !strings.Contains(err.Error(), "bucket") {
			t.Fatalf("want an error about the bucket, got %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("startup took too long to give up on an unreachable store")
		}
	})

	t.Run("manifest path unusable", func(t *testing.T) {
		cfg := testConfig(t, storagetest.NewMem())
		cfg.ManifestPath = t.TempDir() // a directory, not a file
		if _, err := Start(ctx, cfg); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// Against the real S3 server (skipped if it isn't running): the same startup
// path main uses, including the bucket check.
func TestStartAgainstRealS3(t *testing.T) {
	probe := storagetest.NewS3(t) // skips when no server; creates and later deletes a bucket
	_ = probe
	cfg := testConfig(t, nil)
	cfg.S3 = storage.S3Config{
		Endpoint: "http://localhost:9000", Bucket: "strata-app-" + fmt.Sprint(time.Now().UnixNano()),
		AccessKey: "strata-dev", SecretKey: "strata-dev-secret", PathStyle: true,
	}
	// Without CreateBucket, a missing bucket is a startup error...
	if _, err := Start(ctx, cfg); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want 'does not exist', got %v", err)
	}
	// ...with it, the server starts and works.
	cfg.CreateBucket = true
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a, "s3", 120)
	shutdown(t, a)

	s3, _ := storage.NewS3(ctx, cfg.S3)
	cfg.Storage = s3
	msgs, _ := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 120)
	keys, _ := s3.List(ctx, "")
	for _, k := range keys {
		s3.Delete(ctx, k)
	}
	s3.DeleteBucket(ctx)
}

// ---- manifest / bucket identity -------------------------------------------

func objects(t *testing.T, s storage.Storage, prefix string) []string {
	t.Helper()
	keys, err := s.List(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// The disaster this guards against: the manifest file is lost (a container
// restarted without its volume) while the bucket still holds all the data. A
// fresh manifest knows none of those segments, so garbage collection would
// treat them all as orphans and delete them. Strata must refuse to start.
func TestLostManifestIsRefusedInsteadOfDestroyingData(t *testing.T) {
	store := storagetest.NewMem()
	cfg := testConfig(t, store)
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ingestLines(t, a, "precious", 300)
	shutdown(t, a)
	before := objects(t, store, "")
	if len(before) < 1 { // at least one segment (plus the identity marker)
		t.Fatalf("setup: %v", before)
	}

	// "Lose" the manifest: start again with a brand-new manifest file.
	lost := cfg
	lost.ManifestPath = filepath.Join(t.TempDir(), "fresh-manifest.db")
	lost.CompactEnabled = true
	lost.CompactInterval = 10 * time.Millisecond
	lost.CompactSmallBytes, lost.CompactTargetBytes = 1<<20, 8<<20
	lost.OrphanGrace = time.Nanosecond // GC would delete everything if it ever ran
	_, err = Start(ctx, lost)
	if err == nil || !strings.Contains(err.Error(), "different manifest") || !strings.Contains(err.Error(), "adopt-bucket") {
		t.Fatalf("want a refusal that explains the problem and the way out, got %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if after := objects(t, store, ""); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("storage changed:\n before %v\n after  %v", before, after)
	}

	// The original manifest still works, with all the data.
	a2, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("original manifest no longer starts: %v", err)
	}
	shutdown(t, a2)
	msgs, _ := readBack(t, cfg)
	assertExactlyOnce(t, msgs, 300)
}

// -adopt-bucket is the deliberate way out: start fresh on a used bucket,
// accepting that the old segments will be removed as orphans.
func TestAdoptBucketOverridesTheRefusal(t *testing.T) {
	store := storagetest.NewMem()
	cfg := testConfig(t, store)
	a, _ := Start(ctx, cfg)
	ingestLines(t, a, "old", 50)
	shutdown(t, a)

	fresh := cfg
	fresh.ManifestPath = filepath.Join(t.TempDir(), "fresh.db")
	fresh.AdoptBucket = true
	b, err := Start(ctx, fresh)
	if err != nil {
		t.Fatalf("adopt should start: %v", err)
	}
	ingestLines(t, b, "new", 20)
	shutdown(t, b)

	// The marker now belongs to the new manifest, so a later start without the
	// flag is fine, and the old manifest is now the mismatched one.
	c, err := Start(ctx, fresh)
	if err != nil {
		t.Fatalf("restart after adopting: %v", err)
	}
	shutdown(t, c)
	if _, err := Start(ctx, cfg); err == nil {
		t.Fatal("the previous manifest should now be refused")
	}
	msgs, _ := readBack(t, fresh)
	assertExactlyOnce(t, msgs, 20)
}

func TestBucketWithSegmentsButNoMarkerAndEmptyManifestIsRefused(t *testing.T) {
	store := storagetest.NewMem()
	cfg := testConfig(t, store)
	a, _ := Start(ctx, cfg)
	ingestLines(t, a, "x", 10)
	shutdown(t, a)
	store.Delete(ctx, identityKey) // marker gone too

	fresh := cfg
	fresh.ManifestPath = filepath.Join(t.TempDir(), "fresh.db")
	if _, err := Start(ctx, fresh); err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// A deployment from before the marker existed: the manifest has history, the
// bucket has no marker. That is fine; the marker is simply written.
func TestMissingMarkerWithAnEstablishedManifestIsWritten(t *testing.T) {
	store := storagetest.NewMem()
	cfg := testConfig(t, store)
	a, _ := Start(ctx, cfg)
	ingestLines(t, a, "x", 10)
	shutdown(t, a)
	store.Delete(ctx, identityKey)

	b, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("an established manifest must start without a marker: %v", err)
	}
	shutdown(t, b)
	if _, found, _ := readMarker(ctx, store); !found {
		t.Fatal("marker was not written")
	}
}

func TestNewBucketGetsAMarkerAndSegmentListingsIgnoreIt(t *testing.T) {
	store := storagetest.NewMem()
	cfg := testConfig(t, store)
	a, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	shutdown(t, a)
	if _, found, _ := readMarker(ctx, store); !found {
		t.Fatal("no marker written for a new bucket")
	}
	if segs := objects(t, store, "segments/"); len(segs) != 0 {
		t.Fatalf("marker leaked into the segments prefix: %v", segs)
	}
}
