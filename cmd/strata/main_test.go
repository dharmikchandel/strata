package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "strata")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// The real binary, started the way an operator would start it, against the
// real S3 server: serve, accept logs, stop on SIGTERM (what Docker sends) and
// lose nothing that was acknowledged.
func TestBinaryServesAndStopsCleanlyOnSIGTERM(t *testing.T) {
	probe := storagetest.NewS3(t) // skips if no S3 server; we only need the skip check
	_ = probe
	bin := buildBinary(t)
	addr := freeAddr(t)
	dir := t.TempDir()
	bucket := fmt.Sprintf("strata-bin-%d", time.Now().UnixNano())

	var stderr bytes.Buffer
	cmd := exec.Command(bin, "-listen", addr, "-manifest", filepath.Join(dir, "manifest.db"),
		"-s3-endpoint", "http://localhost:9000", "-s3-bucket", bucket, "-s3-create-bucket", "-log-level", "info")
	cmd.Env = append(os.Environ(), "STRATA_S3_ACCESS_KEY=strata-dev", "STRATA_S3_SECRET_KEY=strata-dev-secret")
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Wait until the server reports itself healthy.
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, cancel := context.WithTimeout(ctx, time.Second)
		r, err := healthpb.NewHealthClient(conn).Check(c, &healthpb.HealthCheckRequest{})
		cancel()
		if err == nil && r.Status == healthpb.HealthCheckResponse_SERVING {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("binary exited during startup: %v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	st, err := stratav1.NewIngestServiceClient(conn).Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var entries []*stratav1.LogEntry
	for i := 0; i < 200; i++ {
		entries = append(entries, &stratav1.LogEntry{TimestampUnixNano: int64(1000 + i), Message: fmt.Sprintf("binary-%03d common", i)})
	}
	st.Send(&stratav1.IngestRequest{BatchId: 1, Entries: entries})
	if resp, err := st.Recv(); err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("%v %v", resp, err)
	}

	// Stop it the way Docker does. Nothing has been sealed yet (default buffer
	// settings), so everything depends on the flush during shutdown.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("binary did not exit cleanly: %v\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("binary did not stop after SIGTERM\n%s", stderr.String())
	}

	// Read what it left behind, from a fresh process's point of view.
	s3, err := storage.NewS3(ctx, storage.S3Config{Endpoint: "http://localhost:9000", Bucket: bucket,
		AccessKey: "strata-dev", SecretKey: "strata-dev-secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		keys, _ := s3.List(ctx, "")
		for _, k := range keys {
			s3.Delete(ctx, k)
		}
		s3.DeleteBucket(ctx)
	}()
	m, err := manifest.Open(filepath.Join(dir, "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	res, err := query.New(m, s3, 4).Search(ctx, query.Query{Text: "common", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 200 {
		t.Fatalf("after SIGTERM found %d entries, want 200\n%s", len(res.Hits), stderr.String())
	}
	if log := stderr.String(); !strings.Contains(log, "shutting down") || !strings.Contains(log, "stopped") {
		t.Fatalf("expected shutdown messages in the log:\n%s", log)
	}
}

func TestBinaryRejectsBadConfigWithExitCode1(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-buffer-bytes", "0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		t.Fatalf("want exit code 1, got %v", err)
	}
	if !strings.Contains(stderr.String(), "buffer") {
		t.Fatalf("error message does not say what is wrong: %s", stderr.String())
	}
}

func TestBinaryFailsFastWhenStorageIsUnreachable(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-s3-endpoint", "http://127.0.0.1:1", "-manifest", filepath.Join(t.TempDir(), "m.db"))
	cmd.Env = append(os.Environ(), "STRATA_S3_ACCESS_KEY=k", "STRATA_S3_SECRET_KEY=s")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("want exit code 1, got %v", err)
	}
	if !strings.Contains(stderr.String(), "bucket") {
		t.Fatalf("error message: %s", stderr.String())
	}
	if time.Since(start) > 60*time.Second {
		t.Fatal("took too long to give up")
	}
}

func TestBinaryHelpExitsZero(t *testing.T) {
	out, err := exec.Command(buildBinary(t), "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h should exit 0, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "STRATA_S3_BUCKET") {
		t.Fatalf("usage text missing:\n%s", out)
	}
}
