package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// startGRPC runs the ingest server on a real localhost TCP port and returns a
// client for it. The server is stopped when the test ends.
func startGRPC(t *testing.T, buf *Buffer, cfg ServerConfig) (stratav1.IngestServiceClient, *grpc.Server) {
	t.Helper()
	cfg.Logger = quiet
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewGRPCServer(buf, cfg)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return stratav1.NewIngestServiceClient(conn), srv
}

func pbEntries(prefix string, n int) []*stratav1.LogEntry {
	out := make([]*stratav1.LogEntry, n)
	for i := range out {
		out[i] = &stratav1.LogEntry{
			TimestampUnixNano: int64(1000 + i),
			Message:           fmt.Sprintf("%s-%03d grpc log line", prefix, i),
			Tags:              map[string]string{"src": "test"},
		}
	}
	return out
}

// roundTrip sends one batch and waits for its reply.
func roundTrip(t *testing.T, st stratav1.IngestService_IngestClient, id uint64, entries []*stratav1.LogEntry) *stratav1.IngestResponse {
	t.Helper()
	if err := st.Send(&stratav1.IngestRequest{BatchId: id, Entries: entries}); err != nil {
		t.Fatalf("send batch %d: %v", id, err)
	}
	resp, err := st.Recv()
	if err != nil {
		t.Fatalf("recv batch %d: %v", id, err)
	}
	if resp.BatchId != id {
		t.Fatalf("reply for batch %d, sent %d", resp.BatchId, id)
	}
	return resp
}

func newBuf(t *testing.T, mem *storagetest.Mem, maxBytes int) *Buffer {
	t.Helper()
	b, err := NewBuffer(Config{Storage: mem, MaxBytes: maxBytes, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGRPCStreamLandsInSealedSegments(t *testing.T) {
	mem := storagetest.NewMem()
	buf := newBuf(t, mem, 2000) // small: several segments get sealed along the way
	client, srv := startGRPC(t, buf, ServerConfig{})

	st, err := client.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const batches, perBatch = 20, 25
	for i := 0; i < batches; i++ {
		resp := roundTrip(t, st, uint64(i), pbEntries(fmt.Sprintf("b%02d", i), perBatch))
		if resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK || resp.Accepted != perBatch {
			t.Fatalf("batch %d: %+v", i, resp)
		}
	}
	st.CloseSend()
	if _, err := st.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("want clean EOF after CloseSend, got %v", err)
	}

	// Orderly shutdown: stop the server, then flush the buffer.
	srv.GracefulStop()
	if err := buf.Close(ctx); err != nil {
		t.Fatal(err)
	}

	es, n := readAll(t, mem)
	if len(es) != batches*perBatch {
		t.Fatalf("want %d entries, found %d", batches*perBatch, len(es))
	}
	if n < 2 {
		t.Fatalf("expected several segments, got %d", n)
	}
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.Message] {
			t.Fatalf("duplicate %q", e.Message)
		}
		seen[e.Message] = true
		if e.Tags["src"] != "test" {
			t.Fatalf("tags lost: %+v", e)
		}
	}
	t.Logf("%d entries arrived in %d segments", len(es), n)
}

// An invalid entry rejects its whole batch, leaves the stream usable, and
// nothing from the rejected batch is stored.
func TestGRPCInvalidBatchIsRejectedAtomically(t *testing.T) {
	mem := storagetest.NewMem()
	buf := newBuf(t, mem, 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{})
	st, _ := client.Ingest(ctx)

	bad := append(pbEntries("rejected", 2), &stratav1.LogEntry{Message: "x", Tags: map[string]string{"a=b": "c"}})
	resp := roundTrip(t, st, 1, bad)
	if resp.Status != stratav1.IngestStatus_INGEST_STATUS_INVALID || resp.Accepted != 0 || resp.Message == "" {
		t.Fatalf("got %+v", resp)
	}
	resp = roundTrip(t, st, 2, pbEntries("good", 2))
	if resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("stream unusable after a rejected batch: %+v", resp)
	}
	st.CloseSend()
	buf.Close(ctx)

	for _, e := range mustAll(t, mem) {
		if strings.HasPrefix(e.Message, "rejected") {
			t.Fatalf("entry from a rejected batch was stored: %q", e.Message)
		}
	}
	if got := len(mustAll(t, mem)); got != 2 {
		t.Fatalf("want 2 stored entries, got %d", got)
	}
}

func mustAll(t *testing.T, mem *storagetest.Mem) []segment.Entry {
	t.Helper()
	es, _ := readAll(t, mem)
	return es
}

func TestGRPCBatchSizeLimit(t *testing.T) {
	buf := newBuf(t, storagetest.NewMem(), 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{MaxBatchEntries: 3})
	st, _ := client.Ingest(ctx)
	if r := roundTrip(t, st, 1, pbEntries("a", 4)); r.Status != stratav1.IngestStatus_INGEST_STATUS_INVALID {
		t.Fatalf("4 entries over a limit of 3: %+v", r)
	}
	if r := roundTrip(t, st, 2, pbEntries("a", 3)); r.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("3 entries: %+v", r)
	}
}

// A message over the byte limit is refused by gRPC itself, ending the stream
// with ResourceExhausted.
func TestGRPCOversizedMessageIsRefused(t *testing.T) {
	buf := newBuf(t, storagetest.NewMem(), 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{MaxRecvMsgBytes: 1024})
	st, _ := client.Ingest(ctx)
	huge := []*stratav1.LogEntry{{Message: strings.Repeat("x", 8192)}}
	st.Send(&stratav1.IngestRequest{BatchId: 1, Entries: huge})
	_, err := st.Recv()
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("want ResourceExhausted, got %v", err)
	}
}

func TestGRPCUnsetTimestampGetsServerTime(t *testing.T) {
	mem := storagetest.NewMem()
	buf := newBuf(t, mem, 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{})
	st, _ := client.Ingest(ctx)
	before := time.Now().UnixNano()
	roundTrip(t, st, 1, []*stratav1.LogEntry{{Message: "no timestamp"}})
	after := time.Now().UnixNano()
	st.CloseSend()
	buf.Close(ctx)
	es := mustAll(t, mem)
	if len(es) != 1 || es[0].Timestamp < before || es[0].Timestamp > after {
		t.Fatalf("got %+v (window %d..%d)", es, before, after)
	}
}

// Storage is down and the buffer fills up. The server must say BUFFER_FULL
// (not crash, not silently drop), keep the stream open, and once storage is
// back, a retry of the rejected batch must work with no loss or duplicates.
func TestGRPCBackpressureAndRecovery(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPuts(1 << 20)
	buf := newBuf(t, mem, 300)
	client, _ := startGRPC(t, buf, ServerConfig{})
	st, _ := client.Ingest(ctx)

	var acked []string
	var rejected []*stratav1.LogEntry
	for i := 0; i < 200 && rejected == nil; i++ {
		entries := pbEntries(fmt.Sprintf("b%03d", i), 3)
		resp := roundTrip(t, st, uint64(i), entries)
		switch resp.Status {
		case stratav1.IngestStatus_INGEST_STATUS_OK:
			for _, e := range entries {
				acked = append(acked, e.Message)
			}
		case stratav1.IngestStatus_INGEST_STATUS_BUFFER_FULL:
			if resp.Accepted != 0 {
				t.Fatalf("rejected batch reports accepted=%d", resp.Accepted)
			}
			rejected = entries
		default:
			t.Fatalf("unexpected %+v", resp)
		}
	}
	if rejected == nil {
		t.Fatal("never got BUFFER_FULL while storage was down")
	}

	mem.FailNextPuts(0) // storage recovers
	if err := buf.Flush(ctx); err != nil {
		t.Fatalf("flush after recovery: %v", err)
	}
	// The same stream still works, and the retry is accepted.
	if resp := roundTrip(t, st, 999, rejected); resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		t.Fatalf("retry after recovery: %+v", resp)
	}
	for _, e := range rejected {
		acked = append(acked, e.Message)
	}
	st.CloseSend()
	if err := buf.Close(ctx); err != nil {
		t.Fatal(err)
	}

	stored := map[string]int{}
	for _, e := range mustAll(t, mem) {
		stored[e.Message]++
	}
	for _, m := range acked {
		if stored[m] != 1 {
			t.Fatalf("%q stored %d times, want exactly once", m, stored[m])
		}
	}
	if len(stored) != len(acked) {
		t.Fatalf("stored %d distinct entries, acked %d", len(stored), len(acked))
	}
}

// A client that disconnects after being acked must not lose its data: an ack
// means "in the server's buffer", and the server keeps it.
func TestGRPCAckedDataSurvivesClientDisconnect(t *testing.T) {
	mem := storagetest.NewMem()
	buf := newBuf(t, mem, 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{})

	cctx, cancel := context.WithCancel(ctx)
	st, _ := client.Ingest(cctx)
	roundTrip(t, st, 1, pbEntries("acked", 5))
	cancel() // client vanishes abruptly
	time.Sleep(50 * time.Millisecond)

	if err := buf.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(mustAll(t, mem)); got != 5 {
		t.Fatalf("want 5 entries, got %d", got)
	}
}

// Once the buffer is closed (server shutting down), clients get Unavailable
// so they know to reconnect or fail over, instead of a hang or a silent drop.
func TestGRPCUnavailableWhenBufferClosed(t *testing.T) {
	buf := newBuf(t, storagetest.NewMem(), 1<<30)
	client, _ := startGRPC(t, buf, ServerConfig{})
	st, _ := client.Ingest(ctx)
	buf.Close(ctx)
	st.Send(&stratav1.IngestRequest{BatchId: 1, Entries: pbEntries("late", 1)})
	if _, err := st.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}

func TestGRPCManyConcurrentClients(t *testing.T) {
	const clients, batches, perBatch = 8, 20, 50
	mem := storagetest.NewMem()
	buf := newBuf(t, mem, 4<<10)
	client, srv := startGRPC(t, buf, ServerConfig{})

	var wg sync.WaitGroup
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			st, err := client.Ingest(ctx)
			if err != nil {
				t.Errorf("client %d: %v", c, err)
				return
			}
			for i := 0; i < batches; i++ {
				if err := st.Send(&stratav1.IngestRequest{BatchId: uint64(i), Entries: pbEntries(fmt.Sprintf("c%d-b%02d", c, i), perBatch)}); err != nil {
					t.Errorf("client %d send: %v", c, err)
					return
				}
				resp, err := st.Recv()
				if err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK || resp.BatchId != uint64(i) {
					t.Errorf("client %d batch %d: %+v %v", c, i, resp, err)
					return
				}
			}
			st.CloseSend()
		}(c)
	}
	wg.Wait()
	srv.GracefulStop()
	if err := buf.Close(ctx); err != nil {
		t.Fatal(err)
	}

	es, n := readAll(t, mem)
	if len(es) != clients*batches*perBatch {
		t.Fatalf("want %d entries, got %d", clients*batches*perBatch, len(es))
	}
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.Message] {
			t.Fatalf("duplicate %q", e.Message)
		}
		seen[e.Message] = true
	}
	t.Logf("%d entries from %d clients in %d segments", len(es), clients, n)
}

func TestRecoverInterceptorTurnsPanicIntoInternalError(t *testing.T) {
	intercept := recoverInterceptor(quiet)
	err := intercept(nil, nil, &grpc.StreamServerInfo{FullMethod: "/x"}, func(any, grpc.ServerStream) error {
		panic("boom")
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("want Internal, got %v", err)
	}
}
