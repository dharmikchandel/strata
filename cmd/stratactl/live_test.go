package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/app"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

// liveServer is a real Strata server (in memory, sealing every 30 ms) and the
// page server in front of it, with live views shortened for tests.
type liveServer struct {
	t      *testing.T
	ingest stratav1.IngestService_IngestClient
	batch  uint64
	web    *httptest.Server
	h      *uiHandler
}

func newLiveServer(t *testing.T, live liveConfig) *liveServer {
	t.Helper()
	srv, err := app.Start(context.Background(), app.Config{
		ListenAddr: "127.0.0.1:0", ManifestPath: filepath.Join(t.TempDir(), "m.db"), Storage: storagetest.NewMem(),
		BufferBytes: 1 << 30, BufferAge: 30 * time.Millisecond, MaxRecvMsgBytes: 4 << 20,
		ShutdownGrace: time.Second, OrphanGrace: time.Hour, LogLevel: "error",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	conn, err := dial(srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	stream, err := stratav1.NewIngestServiceClient(conn).Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newUIHandler(stratav1.NewQueryServiceClient(conn), uiOptions{})
	h.live = live
	h.liveSlots = make(chan struct{}, live.MaxViews)
	// A real HTTP server with a short WriteTimeout: a live view must outlast it.
	web := httptest.NewUnstartedServer(h)
	web.Config.WriteTimeout = 300 * time.Millisecond
	web.Start()
	t.Cleanup(web.Close)
	return &liveServer{t: t, ingest: stream, web: web, h: h}
}

// add stores lines "<word> <i>" for i in [from, to), stamped base+i ms.
func (s *liveServer) add(word string, base time.Time, from, to int) {
	s.t.Helper()
	s.batch++
	req := &stratav1.IngestRequest{BatchId: s.batch}
	for i := from; i < to; i++ {
		req.Entries = append(req.Entries, &stratav1.LogEntry{
			TimestampUnixNano: base.Add(time.Duration(i) * time.Millisecond).UnixNano(), Message: fmt.Sprintf("%s %d", word, i),
		})
	}
	if err := s.ingest.Send(req); err != nil {
		s.t.Fatal(err)
	}
	if resp, err := s.ingest.Recv(); err != nil || resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
		s.t.Fatalf("ingest: %v %v", resp, err)
	}
}

// waitSearchable waits until n lines with word are sealed and searchable.
func (s *liveServer) waitSearchable(word string, n int) {
	s.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var out searchJSON
		json.Unmarshal(get(s.t, s.h, "/api/search?limit=1024&text="+word).Body.Bytes(), &out)
		if len(out.Hits) >= n {
			return
		}
	}
	s.t.Fatalf("%d %q lines never became searchable", n, word)
}

type sse struct {
	event, id string
	data      []byte
}

// open starts a live view and returns its events as they arrive.
func (s *liveServer) open(path, lastID string) (<-chan sse, *http.Response) {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.web.URL+path, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { resp.Body.Close() })
	ch := make(chan sse, 100)
	go func() {
		defer close(ch)
		var ev sse
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				ch <- ev
				ev = sse{}
			case strings.HasPrefix(line, "event: "):
				ev.event = line[7:]
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "data: "):
				ev.data = []byte(line[6:])
			}
		}
	}()
	return ch, resp
}

func nextTick(t *testing.T, ch <-chan sse) (liveEvent, string) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		if ev.event != "tick" {
			t.Fatalf("event %q: %s", ev.event, ev.data)
		}
		var out liveEvent
		if err := json.Unmarshal(ev.data, &out); err != nil {
			t.Fatal(err)
		}
		return out, ev.id
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	panic("unreachable")
}

// A live view sends only lines that arrive after it opened, each exactly
// once, newest first, although every search looks back over the overlap; and
// it keeps going past the server's WriteTimeout, ending when its lifetime is up.
func TestLiveSendsEachNewLineOnce(t *testing.T) {
	s := newLiveServer(t, liveConfig{Every: 40 * time.Millisecond, Overlap: time.Minute, Batch: 200, Lifetime: 1500 * time.Millisecond, MaxViews: 4})
	base := time.Now().Add(-time.Second)
	s.add("old", base, 0, 30)
	s.waitSearchable("old", 30)

	ch, resp := s.open("/api/live", "")
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	first, _ := nextTick(t, ch)
	if len(first.Lines) != 0 || first.Corpus == nil || first.Corpus.Entries != 30 || first.Result.Metrics["segments_considered"] == 0 {
		t.Fatalf("first event should carry what is stored and no lines: %+v", first)
	}

	// New lines, stamped after the old ones, in two batches.
	s.add("new", base, 100, 120)
	s.add("new", base, 120, 140)
	got := map[string]int{}
	var order []string
	var lastID string
	start := time.Now()
	for len(got) < 40 || time.Since(start) < 600*time.Millisecond { // past the 300 ms WriteTimeout
		ev, id := nextTick(t, ch)
		lastID = id
		for _, l := range ev.Lines {
			got[l.Message]++
			order = append(order, l.Message)
		}
		if ev.Gap {
			t.Fatal("gap reported with room to spare")
		}
	}
	for msg, n := range got {
		if n != 1 || !strings.HasPrefix(msg, "new ") {
			t.Fatalf("line %q sent %d times", msg, n)
		}
	}
	if len(got) != 40 {
		t.Fatalf("got %d new lines, want 40", len(got))
	}
	if want := fmt.Sprint(base.Add(139 * time.Millisecond).UnixNano()); lastID != want {
		t.Fatalf("event id %s, want the newest timestamp %s", lastID, want)
	}
	// Within one event lines are newest first; check one batch's neighbours.
	if i := strings.Index(strings.Join(order, ","), "new 139,new 138"); i < 0 {
		t.Fatalf("not newest first: %v", order[:5])
	}

	// The lifetime ends the stream with an "end" event.
	for ev := range ch {
		if ev.event == "end" {
			return
		}
	}
	t.Fatal("stream closed without an end event")
}

// A browser that reconnects sends the last id it saw; the stream carries on
// from there, so lines that arrived meanwhile are sent, and none twice.
func TestLiveResumesFromLastEventID(t *testing.T) {
	s := newLiveServer(t, liveConfig{Every: 40 * time.Millisecond, Overlap: time.Minute, Batch: 200, Lifetime: time.Minute, MaxViews: 4})
	base := time.Now().Add(-time.Second)
	s.add("line", base, 0, 50)
	s.waitSearchable("line", 50)

	// The header (a reconnect) and the "after" parameter (a page that already
	// shows lines up to there) start the view at the same place.
	at := fmt.Sprint(base.Add(39 * time.Millisecond).UnixNano())
	for _, how := range []string{"header", "after"} {
		var ch <-chan sse
		if how == "header" {
			ch, _ = s.open("/api/live?text=line", at)
		} else {
			ch, _ = s.open("/api/live?text=line&after="+at, "")
		}
		checkResumed(t, how, ch)
	}
}

func checkResumed(t *testing.T, how string, ch <-chan sse) {
	t.Helper()
	ev, _ := nextTick(t, ch)
	if len(ev.Lines) != 10 || ev.Lines[0].Message != "line 49" || ev.Lines[9].Message != "line 40" {
		var msgs []string
		for _, l := range ev.Lines {
			msgs = append(msgs, l.Message)
		}
		t.Fatalf("%s: resumed with %v, want line 49 down to line 40", how, msgs)
	}
}

func TestLiveRefusesBeyondTheViewLimitAndBadInput(t *testing.T) {
	s := newLiveServer(t, liveConfig{Every: time.Second, Overlap: time.Minute, Batch: 10, Lifetime: time.Minute, MaxViews: 1})
	ch, _ := s.open("/api/live", "")
	nextTick(t, ch)
	resp, err := http.Get(s.web.URL + "/api/live")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second view: status %d, want 429", resp.StatusCode)
	}
	for _, bad := range []string{"/api/live?tag=novalue", "/api/live?after=yesterday", "/api/live?limit=0", "/api/live?limit=1025"} {
		if rec := get(t, s.h, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", bad, rec.Code)
		}
	}
}
