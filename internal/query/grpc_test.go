package query

import (
	"bytes"
	"context"
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
	"github.com/dharmikchandel/strata/internal/storage"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func startQueryServer(t *testing.T, eng *Engine, cfg ServiceConfig) stratav1.QueryServiceClient {
	t.Helper()
	cfg.Logger = quiet
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	stratav1.RegisterQueryServiceServer(srv, NewService(eng, cfg))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return stratav1.NewQueryServiceClient(conn)
}

func TestSearchRPCReturnsHitsAndMetrics(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(10)
	client := startQueryServer(t, e.eng, ServiceConfig{})

	resp, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common", FromUnixNano: 3000, ToUnixNano: 4000})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Metrics
	if len(resp.Hits) != 100 || resp.Truncated {
		t.Fatalf("hits=%d truncated=%v", len(resp.Hits), resp.Truncated)
	}
	if m.SegmentsConsidered != 10 || m.SkippedByTime != 9 || m.SegmentsScanned != 1 || m.BytesRead != uint64(rows[3].Size) {
		t.Fatalf("metrics: %+v", m)
	}
	h := resp.Hits[0]
	if h.TimestampUnixNano != 3000 || h.SegmentId != rows[3].ID || !strings.Contains(h.Message, "seg3") {
		t.Fatalf("first hit: %+v", h)
	}
	if resp.Hits[0].Tags["env"] != "prod" {
		t.Fatalf("tags lost: %+v", resp.Hits[0].Tags)
	}

	// Tag filter and limit go through too.
	resp, err = client.Search(ctx, &stratav1.SearchRequest{Text: "seg2", Tags: map[string]string{"env": "prod"}, Limit: 5})
	if err != nil || len(resp.Hits) != 5 || !resp.Truncated {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestSearchRPCRejectsBadRequests(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(1)
	client := startQueryServer(t, e.eng, ServiceConfig{})
	cases := map[string]*stratav1.SearchRequest{
		"limit too large": {Text: "x", Limit: MaxLimit + 1},
		"text too long":   {Text: strings.Repeat("a", MaxTextBytes+1)},
		"negative from":   {FromUnixNano: -1},
		"from after to":   {FromUnixNano: 10, ToUnixNano: 5},
		"from equals to":  {FromUnixNano: 10, ToUnixNano: 10},
	}
	for name, req := range cases {
		if _, err := client.Search(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: want InvalidArgument, got %v", name, err)
		}
	}
}

// gatedStore holds every Get until released, so tests can keep searches
// "in flight" for as long as they like.
type gatedStore struct {
	storage.Storage
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (g *gatedStore) Get(c context.Context, key string) (io.ReadCloser, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.gate:
	case <-c.Done():
		return nil, c.Err()
	}
	return g.Storage.Get(c, key)
}

// Too many concurrent searches are refused immediately, not queued: a search
// can pull whole segments into memory, so a burst must not be able to use up
// the server's memory.
func TestSearchRPCShedsLoadBeyondTheConcurrencyLimit(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(2)
	gs := &gatedStore{Storage: e.store, gate: make(chan struct{}), started: make(chan struct{})}
	client := startQueryServer(t, New(e.m, gs, 1), ServiceConfig{MaxConcurrent: 1})

	first := make(chan error, 1)
	go func() { _, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common"}); first <- err }()
	<-gs.started // the first search now holds the only slot

	start := time.Now()
	_, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second search: want ResourceExhausted, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the second search waited instead of being refused at once")
	}

	close(gs.gate)
	if err := <-first; err != nil {
		t.Fatalf("first search should still succeed: %v", err)
	}
	// And the slot is free again afterwards.
	if _, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common"}); err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
}

func TestSearchRPCTimeouts(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(2)
	gs := &gatedStore{Storage: e.store, gate: make(chan struct{}), started: make(chan struct{})} // never released
	t.Cleanup(func() { close(gs.gate) })
	client := startQueryServer(t, New(e.m, gs, 1), ServiceConfig{Timeout: 150 * time.Millisecond})

	// No deadline from the client: the server's own timeout applies.
	start := time.Now()
	_, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common"})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("want DeadlineExceeded from the server's timeout, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("search was not cut off")
	}
	// The client's own, shorter deadline is honoured too.
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := client.Search(cctx, &stratav1.SearchRequest{Text: "common"}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

// Damaged or missing data is reported as DataLoss and names the segment; the
// answer isn't silently incomplete.
func TestSearchRPCReportsDamagedData(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(3)
	client := startQueryServer(t, e.eng, ServiceConfig{})

	data := readAll(t, e.store, rows[1].Key)
	data[len(data)/2] ^= 0xFF
	e.store.Put(ctx, rows[1].Key, bytes.NewReader(data))
	_, err := client.Search(ctx, &stratav1.SearchRequest{Text: "common"})
	if status.Code(err) != codes.DataLoss || !strings.Contains(err.Error(), rows[1].ID) {
		t.Fatalf("corrupt segment: want DataLoss naming %s, got %v", rows[1].ID, err)
	}

	e.store.Delete(ctx, rows[2].Key)
	_, err = client.Search(ctx, &stratav1.SearchRequest{Text: "seg2"})
	if status.Code(err) != codes.DataLoss {
		t.Fatalf("missing segment: want DataLoss, got %v", err)
	}
}
