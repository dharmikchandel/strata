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
	"regexp"
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
	req   *stratav1.SearchRequest
	resp  *stratav1.SearchResponse
	err   error
	stats *stratav1.StatsResponse
	sErr  error
}

func (s *stubSearcher) Stats(_ context.Context, _ *stratav1.StatsRequest, _ ...grpc.CallOption) (*stratav1.StatsResponse, error) {
	if s.sErr != nil {
		return nil, s.sErr
	}
	if s.stats == nil {
		return &stratav1.StatsResponse{}, nil
	}
	return s.stats, nil
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
		Segments: []*stratav1.SegmentOutcome{
			{Kind: stratav1.SegmentOutcome_KIND_SKIPPED_BY_TIME, SegmentId: "old", MinUnixNano: time.Date(2005, 6, 3, 15, 42, 50, 0, time.UTC).UnixNano(), MaxUnixNano: time.Date(2005, 6, 4, 1, 2, 3, 0, time.UTC).UnixNano(), SizeBytes: 1000},
			{Kind: stratav1.SegmentOutcome_KIND_SKIPPED_BY_BLOOM, SegmentId: "mid", SizeBytes: 2000},
			{Kind: stratav1.SegmentOutcome_KIND_SCANNED, SegmentId: "hit", SizeBytes: 2048, BytesRead: 2048, Hits: 1},
			{Kind: stratav1.SegmentOutcome_KIND_UNSPECIFIED, SegmentId: "from-a-newer-server"},
		},
		Histogram: &stratav1.Histogram{StartUnixNano: time.Date(2005, 6, 3, 15, 0, 0, 0, time.UTC).UnixNano(), BucketNanos: int64(time.Minute), Counts: []uint64{0, 7, 1}},
	}}
	h, err := newUIHandler(stub, uiOptions{})
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
	// Per-segment results: oldest first as sent, kinds translated for the page,
	// times formatted in UTC, and an outcome kind this page doesn't know is dropped
	// rather than drawn as something it is not.
	if len(out.Segments) != 3 || out.SegmentsTruncated {
		t.Fatalf("segments: %+v", out.Segments)
	}
	kinds := []string{out.Segments[0].Kind, out.Segments[1].Kind, out.Segments[2].Kind}
	if fmt.Sprint(kinds) != "[time bloom scanned]" || out.Segments[0].First != "2005-06-03 15:42" || out.Segments[0].Last != "2005-06-04 01:02" {
		t.Fatalf("segment mapping: %+v", out.Segments)
	}
	if s := out.Segments[2]; s.ID != "hit" || s.BytesRead != 2048 || s.Hits != 1 || s.Size != 2048 {
		t.Fatalf("scanned segment: %+v", s)
	}
	if hg := out.Histogram; hg == nil || hg.StartMs != time.Date(2005, 6, 3, 15, 0, 0, 0, time.UTC).UnixMilli() || hg.BucketMs != 60_000 || fmt.Sprint(hg.Counts) != "[0 7 1]" {
		t.Fatalf("histogram: %+v", out.Histogram)
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
	h, _ := newUIHandler(stub, uiOptions{})
	if rec := get(t, h, "/api/search?text=x"); rec.Code != 200 || stub.req.Limit != 100 {
		t.Fatalf("defaults: status %d limit %d", rec.Code, stub.req.Limit)
	}
	for _, bad := range []string{
		"/api/search?limit=0", "/api/search?limit=1025", "/api/search?limit=abc",
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
		h, _ := newUIHandler(&stubSearcher{err: c.err}, uiOptions{})
		rec := get(t, h, "/api/search?text=x")
		if rec.Code != c.want {
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.want)
		}
		if c.mustHide != "" && strings.Contains(rec.Body.String(), c.mustHide) {
			t.Errorf("%v: internal detail leaked to the visitor: %s", c.err, rec.Body)
		}
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
	h, _ := newUIHandler(stratav1.NewQueryServiceClient(conn), uiOptions{})
	deadline := time.Now().Add(5 * time.Second)
	var out searchJSON
	for {
		rec := get(t, h, "/api/search?text=parity&tag=level%3DINFO&limit=1024")
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

// When the server leaves the segment list out (too many segments), the page
// gets an empty list, never null, plus the flag that tells it to fall back to
// the summary counts.
func TestUIPassesOnAnOmittedSegmentList(t *testing.T) {
	stub := &stubSearcher{resp: &stratav1.SearchResponse{
		SegmentsTruncated: true,
		Metrics:           &stratav1.SearchMetrics{SegmentsConsidered: 5000, SkippedByBloom: 4999, SegmentsScanned: 1},
	}}
	h, _ := newUIHandler(stub, uiOptions{})
	rec := get(t, h, "/api/search?text=x")
	var raw map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["segments"]) != "[]" || string(raw["segments_truncated"]) != "true" {
		t.Fatalf("segments=%s truncated=%s", raw["segments"], raw["segments_truncated"])
	}
	// No histogram from the server (nothing in range) is an explicit null.
	if string(raw["histogram"]) != "null" {
		t.Fatalf("histogram=%s", raw["histogram"])
	}
}

func TestUIServesStaticFilesWithStrictHeaders(t *testing.T) {
	h, _ := newUIHandler(&stubSearcher{}, uiOptions{})
	// Both pages (the landing page at / and the app at /search/) and every
	// file the built pages link to.
	pages := map[string]string{"/": readFile(t, "ui/index.html"), "/search/": readFile(t, "ui/search/index.html")}
	var paths []string
	for url, page := range pages {
		paths = append(paths, url)
		links := regexp.MustCompile(`(?:src|href)="(/[^"#]+)"`).FindAllStringSubmatch(page, -1)
		if len(links) < 3 {
			t.Fatalf("%s links to %v; expected its script and stylesheets (run make web-build)", url, links)
		}
		for _, m := range links {
			paths = append(paths, m[1])
		}
		// The CSP allows no inline code, so the built pages must not contain any.
		if regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]|<style|\sstyle="`).MatchString(page) {
			t.Errorf("the built %s has an inline script or style, which the CSP blocks", url)
		}
	}
	// The app's address without the slash must still reach it.
	if rec := get(t, h, "/search"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "search/" && rec.Header().Get("Location") != "/search/" {
		t.Errorf("/search: %d %q, want a redirect to /search/", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range paths {
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
	for path, src := range webSources(t, ".ts", ".tsx") {
		if strings.Contains(src, "dangerouslySetInnerHTML") || strings.Contains(src, "innerHTML") {
			t.Errorf("%s inserts HTML; log lines are untrusted and must be rendered as text", path)
		}
	}
	if rec := get(t, h, "/../../etc/passwd"); rec.Code == 200 && strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("path traversal")
	}
}

// The legend colours once leaked onto table cells: a bare `.time` rule meant
// for a swatch also matched the timestamp cells (class "time"), which got a
// grey background and 3:1 text. Component styles are now CSS modules, whose
// class names are made unique at build time; only the small global files may
// define plain classes, and only the few listed here.
func TestStylesAreScopedToTheirComponents(t *testing.T) {
	globalOK := map[string]bool{"mono": true, "sr-only": true, "display": true}
	for path, css := range webSources(t, ".css") {
		if strings.HasSuffix(path, ".module.css") {
			continue
		}
		if !strings.Contains(path, "/styles/") {
			t.Errorf("%s: component styles must be CSS modules (*.module.css)", path)
			continue
		}
		for _, m := range regexp.MustCompile(`(?m)^[^{}@/*]*?\.([a-zA-Z][\w-]*)[^{}]*\{`).FindAllStringSubmatch(stripCSSComments(css), -1) {
			if !globalOK[m[1]] {
				t.Errorf("%s defines a global class .%s; move it into a component's CSS module", path, m[1])
			}
		}
	}
}

// Phone adaptation guards. These check the things that went wrong on phones and
// that no desktop-width test would notice.
func TestPageIsAdaptedForTouchAndSmallScreens(t *testing.T) {
	page := readFile(t, "ui/index.html") + readFile(t, "ui/search/index.html")
	tsx := webSources(t, ".tsx")
	css := webSources(t, ".css")
	all := func(m map[string]string) string {
		var b strings.Builder
		for _, v := range m {
			b.WriteString(v)
		}
		return b.String()
	}
	allTSX, allCSS := all(tsx), all(css)
	file := func(name string) string {
		for path, src := range tsx {
			if strings.HasSuffix(path, "/"+name) {
				return src
			}
		}
		t.Fatalf("web source %s not found", name)
		return ""
	}

	// A cursor in the search box opens the on-screen keyboard over the result.
	if strings.Contains(page, "autofocus") || strings.Contains(allTSX, "autoFocus") {
		t.Error("autofocus opens the keyboard on phones; focus only for fine pointers, from script")
	}
	if !strings.Contains(file("App.tsx"), "(pointer: fine)") {
		t.Error("the page should focus the search box only on pointer devices")
	}
	// Notch / rounded-corner safe areas need viewport-fit=cover to take effect.
	if strings.Count(page, "viewport-fit=cover") < 2 || !strings.Contains(allCSS, "safe-area-inset") {
		t.Error("the page should extend under the notch and pad by the safe-area insets")
	}
	// Log search terms must not be auto-capitalised or autocorrected.
	form := file("QueryBar.tsx")
	for _, want := range []string{`autoCapitalize: 'off'`, `autoCorrect: 'off'`, `spellCheck: false`, `enterKeyHint="search"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the search input is missing %s", want)
		}
	}
	// Rows become blocks on phones, which drops table semantics unless roles are explicit.
	for _, name := range []string{"LogViewer.tsx", "ReadList.tsx"} {
		src := file(name)
		for _, role := range []string{`role="table"`, `role="rowgroup"`, `role="row"`, `role="columnheader"`, `role="cell"`} {
			if !strings.Contains(src, role) {
				t.Errorf("%s is missing %s", name, role)
			}
		}
	}
	// The time control must point at the panel it opens and say whether it is open.
	timeRange := file("TimeRange.tsx")
	if !strings.Contains(timeRange, `aria-controls="time-range"`) || !strings.Contains(timeRange, `id="time-range"`) || !strings.Contains(timeRange, `aria-expanded=`) {
		t.Error("the time control needs aria-controls and aria-expanded")
	}
	for path, src := range css {
		src = stripCSSComments(src)
		// Core information must never be hidden on a phone: the tags column was, once.
		if regexp.MustCompile(`\.tags?\b[^{]*\{[^}]*display:\s*none`).MatchString(src) {
			t.Errorf("%s: a rule hides the tags; they are core information", path)
		}
		// A bare `th:nth-child(n)` rule once hid a column of another table.
		if regexp.MustCompile(`(?m)(^|[{};])\s*th:nth-child`).MatchString(src) {
			t.Errorf("%s: th:nth-child selectors must be scoped to one table", path)
		}
		blocks := cssBlocks(src)
		for i, b := range blocks {
			// Hover styles belong inside a hover media query, so a tap leaves nothing "stuck".
			if strings.Contains(b.selector, ":hover") && !strings.Contains(b.within, "(hover: hover)") {
				t.Errorf("%s: %q is not inside @media (hover: hover)", path, b.selector)
			}
			// Touch sizing must come after the base rules it overrides: it once
			// sat before them, lost on order, and left rows 42px tall on touch.
			if strings.Contains(b.within, "(pointer: coarse)") {
				for _, later := range blocks[i+1:] {
					if !strings.Contains(later.within, "(pointer: coarse)") {
						t.Errorf("%s: %q comes after the (pointer: coarse) block; touch sizes must be last so they win", path, later.selector)
						break
					}
				}
			}
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// webSources returns the page's source files (web/src) with the given extensions.
func webSources(t *testing.T, exts ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("../../web/src", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, ext := range exts {
			if strings.HasSuffix(path, ext) && !strings.HasSuffix(path, ".test.ts") {
				out[filepath.ToSlash(path)] = readFile(t, path)
			}
		}
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("reading web/src: %v (%d files)", err, len(out))
	}
	return out
}

func stripCSSComments(css string) string {
	return regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
}

// cssRule is one style rule: its selector and the at-rules it sits inside.
type cssRule struct{ selector, within string }

// cssBlocks lists the style rules of a stylesheet in order. Enough of a parser
// for these guards: it tracks braces and treats every block that holds
// declarations as a rule.
func cssBlocks(css string) []cssRule {
	var rules []cssRule
	var stack []string
	start := 0
	for i, c := range css {
		switch c {
		case '{':
			stack = append(stack, strings.TrimSpace(css[start:i]))
			start = i + 1
		case '}':
			if len(stack) > 0 {
				sel := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !strings.HasPrefix(sel, "@") {
					rules = append(rules, cssRule{selector: sel, within: strings.Join(stack, " ")})
				}
			}
			start = i + 1
		case ';':
			if len(stack) == 0 || strings.HasPrefix(stack[len(stack)-1], "@") {
				start = i + 1 // a statement at top level or directly inside an at-rule
			}
		}
	}
	return rules
}
