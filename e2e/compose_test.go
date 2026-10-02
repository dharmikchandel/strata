//go:build e2e

// End-to-end tests of the Docker setup: they build the image, start the real
// containers with docker compose, and check behaviour from the outside.
//
//	go test -tags e2e -v -timeout 15m ./e2e
//
// They use their own compose project name and ports, so they don't disturb a
// stack you started yourself, and they delete their own volumes afterwards.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/query"
	"github.com/dharmikchandel/strata/internal/storage"
)

const (
	project     = "strata-e2e"
	strataPort  = "17070"
	rustfsPort  = "19000"
	consolePort = "19001"
	bucket      = "strata" // the bucket the compose file configures
)

var ctx = context.Background()

func compose(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", "../docker-compose.yml"}, args...)...)
	cmd.Env = append(os.Environ(), "STRATA_PORT="+strataPort, "RUSTFS_PORT="+rustfsPort, "RUSTFS_CONSOLE_PORT="+consolePort)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustCompose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := compose(t, args...)
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func ingest(t *testing.T, prefix string, n int) {
	t.Helper()
	conn, err := grpc.NewClient("127.0.0.1:"+strataPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := stratav1.NewIngestServiceClient(conn).Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var entries []*stratav1.LogEntry
	for i := 0; i < n; i++ {
		entries = append(entries, &stratav1.LogEntry{TimestampUnixNano: time.Now().UnixNano() + int64(i), Message: fmt.Sprintf("%s-%04d common", prefix, i)})
	}
	if err := st.Send(&stratav1.IngestRequest{BatchId: 1, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	resp, err := st.Recv()
	if err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("batch not accepted: %v %v", resp, err)
	}
	st.CloseSend()
}

// storedEntries copies the manifest out of the (stopped) container's volume and
// searches it together with the bucket, like a fresh process would.
func storedEntries(t *testing.T) int {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "manifest.db")
	mustCompose(t, "cp", "strata:/data/manifest.db", dst)
	m, err := manifest.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s3, err := storage.NewS3(ctx, storage.S3Config{Endpoint: "http://127.0.0.1:" + rustfsPort, Bucket: bucket,
		AccessKey: "strata-dev", SecretKey: "strata-dev-secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := query.New(m, s3, 4).Search(ctx, query.Query{Text: "common", Limit: 100000})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return len(res.Hits)
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := compose(t, "logs", "--tail", "40", "strata")
	t.Fatalf("timed out waiting for %s\n--- strata logs ---\n%s", what, logs)
}

func TestStack(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	t.Cleanup(func() { compose(t, "down", "-v", "--remove-orphans") })
	compose(t, "down", "-v", "--remove-orphans") // clear leftovers of an aborted earlier run

	// 1. Build and start. --wait returns only once the healthchecks pass: the
	// object store answering /health, and `strata -healthcheck` succeeding.
	mustCompose(t, "up", "-d", "--build", "--wait", "--wait-timeout", "180")

	// 2. Ingest, then stop the container the way `docker stop` does. Nothing has
	// been sealed yet (default buffer settings), so the data survives only if the
	// SIGTERM handler flushes it, within the grace period the compose file sets.
	ingest(t, "first", 300)
	start := time.Now()
	mustCompose(t, "stop", "strata")
	t.Logf("stopped in %v", time.Since(start))
	if time.Since(start) > 35*time.Second {
		t.Fatal("container needed the full grace period: shutdown is not finishing promptly")
	}
	exit := strings.TrimSpace(mustCompose(t, "ps", "-a", "--format", "{{.ExitCode}}", "strata"))
	if exit != "0" {
		t.Fatalf("container exit code %q, want 0 (a non-zero code means it was killed or the final flush failed)", exit)
	}
	if got := storedEntries(t); got != 300 {
		t.Fatalf("after docker stop: %d entries stored, want 300", got)
	}

	// 3. Restart the container: state in the volume must carry over.
	mustCompose(t, "start", "strata")
	waitFor(t, "strata healthy after restart", 60*time.Second, func() bool {
		out, _ := compose(t, "ps", "--format", "{{.Health}}", "strata")
		return strings.TrimSpace(out) == "healthy"
	})
	ingest(t, "second", 100)
	mustCompose(t, "stop", "strata")
	if got := storedEntries(t); got != 400 {
		t.Fatalf("after restart: %d entries stored, want 400", got)
	}

	// 4. The disaster: the manifest volume is lost while the bucket survives.
	// Strata must refuse to start and must not touch the data.
	mustCompose(t, "rm", "-sf", "strata")
	if out, err := exec.Command("docker", "volume", "rm", project+"_strata-data").CombinedOutput(); err != nil {
		t.Fatalf("remove volume: %v\n%s", err, out)
	}
	mustCompose(t, "up", "-d", "--no-deps", "strata") // no --wait: it is expected NOT to become healthy
	waitFor(t, "the refusal message", 60*time.Second, func() bool {
		out, _ := compose(t, "logs", "strata")
		return strings.Contains(out, "different manifest")
	})
	s3, _ := storage.NewS3(ctx, storage.S3Config{Endpoint: "http://127.0.0.1:" + rustfsPort, Bucket: bucket,
		AccessKey: "strata-dev", SecretKey: "strata-dev-secret", PathStyle: true})
	keys, err := s3.List(ctx, "segments/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("segments in the bucket after the lost-manifest start: %v (err %v)", keys, err)
	}
	t.Logf("bucket still holds %d segment files after the refused start", len(keys))
}
