package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/app"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

type stubSearcher struct {
	req  *stratav1.SearchRequest
	resp *stratav1.SearchResponse
	err  error
}

func (s *stubSearcher) Search(_ context.Context, in *stratav1.SearchRequest, _ ...grpc.CallOption) (*stratav1.SearchResponse, error) {
	s.req = in
	return s.resp, s.err
}

func get(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	return rec
}

func TestUISearchPassesParametersAndShapesTheResponse(t *testing.T) {
	stub := &stubSearcher{resp: &stratav1.SearchResponse{
		Truncated: true,
		Hits: []*stratav1.SearchHit{{TimestampUnixNano: time.Date(2005, 6, 3, 15, 42, 50, 123456000, time.UTC).UnixNano(),
			Message: "<script>alert(1)</script> parity error", Tags: map[string]string{"level": "INFO"}, SegmentId: "seg1"}},
		Metrics: &stratav1.SearchMetrics{SegmentsConsidered: 10, SkippedByTime: 6, SkippedByBloom: 3, SegmentsScanned: 1, BytesRead: 2048, ServerMicros: 1500},
	}}
	h, err := newUIHandler(stub)
	if err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/api/search?text=parity+error&tag=level%3DINFO&tag=component%3DKERNEL&from=2005-06-03T15:00&to=2005-06-04&limit=25")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if stub.req.Text != "parity error" || stub.req.Limit != 25 || stub.req.Tags["level"] != "INFO" || stub.req.Tags["component"] != "KERNEL" {
		t.Fatalf("request not passed through: %+v", stub.req)
	}
	if want := time.Date(2005, 6, 3, 15, 0, 0, 0, time.UTC).UnixNano(); stub.req.FromUnixNano != want {
		t.Fatalf("from = %d, want %d", stub.req.FromUnixNano, want)
	}
	var out searchJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Time != "2005-06-03 15:42:50.123456" || !out.Truncated {
		t.Fatalf("response: %+v", out)
	}
	if out.Metrics["segments_scanned"] != 1 || out.Metrics["skipped_by_bloom"] != 3 || out.Metrics["bytes_read"] != 2048 {
		t.Fatalf("metrics: %v", out.Metrics)
	}
	// The hostile message travels as data inside JSON; the page inserts it as text.
	if !strings.Contains(out.Hits[0].Message, "<script>") {
		t.Fatal("message was altered")
	}
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", rec.Header())
	}
}

func TestUIDefaultsAndValidation(t *testing.T) {
	stub := &stubSearcher{resp: &stratav1.SearchResponse{}}
	h, _ := newUIHandler(stub)
	if rec := get(t, h, "/api/search?text=x"); rec.Code != 200 || stub.req.Limit != 100 {
		t.Fatalf("defaults: status %d limit %d", rec.Code, stub.req.Limit)
	}
	for _, bad := range []string{
		"/api/search?limit=0", "/api/search?limit=1001", "/api/search?limit=abc",
		"/api/search?tag=novalue", "/api/search?tag=%3Dx",
		"/api/search?from=yesterday", "/api/search?to=2005-13-45",
	} {
		stub.req = nil
		rec := get(t, h, bad)
		if rec.Code != http.StatusBadRequest || stub.req != nil {
			t.Errorf("%s: want 400 without calling the server, got %d", bad, rec.Code)
		}
	}
}

func TestUIMapsServerErrorsToSafeHTTPErrors(t *testing.T) {
	cases := []struct {
		err      error
		want     int
		mustHide string
	}{
		{status.Error(codes.InvalidArgument, "limit too large"), 400, ""},
		{status.Error(codes.ResourceExhausted, "busy"), 429, ""},
		{status.Error(codes.DeadlineExceeded, "slow"), 504, ""},
		{status.Error(codes.Unavailable, "down"), 503, ""},
		{status.Error(codes.DataLoss, "segment 18dac-secret is corrupt"), 502, "18dac-secret"},
		{status.Error(codes.Internal, "panic at /srv/internal/x.go:1"), 500, "/srv/internal"},
		{errors.New("plain error"), 502, "plain error"},
	}
	for _, c := range cases {
		h, _ := newUIHandler(&stubSearcher{err: c.err})
		rec := get(t, h, "/api/search?text=x")
		if rec.Code != c.want {
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.want)
		}
		if c.mustHide != "" && strings.Contains(rec.Body.String(), c.mustHide) {
			t.Errorf("%v: internal detail leaked to the visitor: %s", c.err, rec.Body)
		}
	}
}

func TestUIServesStaticFilesWithStrictHeaders(t *testing.T) {
	h, _ := newUIHandler(&stubSearcher{})
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := get(t, h, path)
		if rec.Code != 200 {
			t.Errorf("%s: %d", path, rec.Code)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "unsafe-inline") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: weak headers: %v", path, rec.Header())
		}
	}
	// The page must never insert server text as HTML.
	js, _ := os.ReadFile("ui/app.js")
	if strings.Contains(string(js), "innerHTML") {
		t.Error("app.js uses innerHTML; log lines are untrusted and must be inserted as text")
	}
	if rec := get(t, h, "/../../etc/passwd"); rec.Code == 200 && strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("path traversal")
	}
}

func TestHelpers(t *testing.T) {
	if got, err := parseTime("2005-06-03T15:00:00Z"); err != nil || got != time.Date(2005, 6, 3, 15, 0, 0, 0, time.UTC).UnixNano() {
		t.Errorf("rfc3339: %d %v", got, err)
	}
	if got, _ := parseTime("123"); got != 123 {
		t.Errorf("unix nanos: %d", got)
	}
	if got, err := parseTime(""); got != 0 || err != nil {
		t.Errorf("empty: %d %v", got, err)
	}
	if _, err := parseTime("tomorrow"); err == nil {
		t.Error("accepted garbage")
	}
	for n, want := range map[uint64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	m := &stratav1.SearchMetrics{SegmentsConsidered: 100, SkippedByTime: 50, SkippedByBloom: 40, SegmentsScanned: 1}
	b := bar(m)
	if strings.Count(b, ".") < 25 || !strings.Contains(b, "#") { // a single read is always visible
		t.Errorf("bar: %q", b)
	}
	tf := tagFlags{}
	if err := tf.Set("level=INFO"); err != nil || tf["level"] != "INFO" {
		t.Error("tag flag")
	}
	if tf.Set("novalue") == nil || tf.Set("=x") == nil {
		t.Error("bad tag accepted")
	}
}

// The whole demo path with a real server: stratactl ingest -> server -> search
// RPC -> web page JSON.
func TestIngestCommandThenSearchThroughTheUI(t *testing.T) {
	cfg := app.Config{
		ListenAddr: "127.0.0.1:0", ManifestPath: filepath.Join(t.TempDir(), "m.db"), Storage: storagetest.NewMem(),
		BufferBytes: 1 << 30, BufferAge: 30 * time.Millisecond, MaxRecvMsgBytes: 4 << 20,
		ShutdownGrace: time.Second, OrphanGrace: time.Hour, LogLevel: "error",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv, err := app.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "- 1117838570 2005.06.03 R02-M1-N0-C:J12-U11 2005-06-03-15.42.%02d.%06d R02-M1-N0-C:J12-U11 RAS KERNEL INFO line number %d parity error\n", i%60, i, i)
	}
	file := filepath.Join(t.TempDir(), "bgl.log")
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runIngest([]string{"-addr", srv.Addr().String(), "-file", file, "-format", "bgl", "-batch", "100"}); err != nil {
		t.Fatalf("ingest command: %v", err)
	}

	conn, err := dial(srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h, _ := newUIHandler(stratav1.NewQueryServiceClient(conn))
	deadline := time.Now().Add(5 * time.Second)
	var out searchJSON
	for {
		rec := get(t, h, "/api/search?text=parity&tag=level%3DINFO&limit=1000")
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		out = searchJSON{}
		json.Unmarshal(rec.Body.Bytes(), &out)
		if len(out.Hits) == 300 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(out.Hits) != 300 || out.Metrics["segments_scanned"] == 0 {
		t.Fatalf("found %d of 300 lines; metrics %v", len(out.Hits), out.Metrics)
	}
	if err := runIngest([]string{"-addr", srv.Addr().String(), "-format", "nonsense"}); err == nil {
		t.Error("bad -format accepted")
	}
}
