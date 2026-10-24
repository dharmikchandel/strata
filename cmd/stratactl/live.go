package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/query"
)

// liveConfig controls /api/live. Tests shorten the times.
type liveConfig struct {
	// Every is how often a live view searches for new lines.
	Every time.Duration
	// Overlap is how far before the newest line already seen each search
	// looks again. Lines become searchable only when their segment is sealed
	// (up to about 5 s after they arrive), and segments from different
	// writers seal at different moments, so a line can turn up after a newer
	// one was already shown. Lines stamped further back than this when they
	// become searchable (late arrivals) are not streamed; a search finds them.
	Overlap time.Duration
	// Batch is the most lines one search sends when the page doesn't say
	// (it asks for as many as it shows); with more new lines than that the
	// newest are sent and the event says some were passed over.
	Batch uint32
	// Lifetime ends a live view, so a forgotten tab doesn't search forever.
	Lifetime time.Duration
	MaxViews int
}

var defaultLive = liveConfig{Every: 2 * time.Second, Overlap: 15 * time.Second, Batch: 200, Lifetime: 15 * time.Minute, MaxViews: 16}

// liveEvent is one message of the stream: the lines that are new since the
// last one (newest first), the search that found them, and what is stored now.
type liveEvent struct {
	Lines []hitJSON `json:"lines"`
	// Gap is true when more new lines arrived than one event carries, so some
	// were passed over.
	Gap    bool        `json:"gap"`
	Result searchJSON  `json:"result"`
	Corpus *corpusJSON `json:"corpus"`
}

// liveTail streams new matching lines as Server-Sent Events: a long-lived
// response the browser's EventSource reads event by event. The stream has no
// state on the Strata server: every few seconds it runs an ordinary search,
// newest first, over the last moments of data, and passes on the lines it
// hasn't sent yet. Each event's id is the newest timestamp sent, which the
// browser sends back (Last-Event-ID) if it has to reconnect, so a reconnect
// carries on from there instead of skipping or repeating lines.
func (h *uiHandler) liveTail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tags, err := parseTags(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	text := q.Get("text")
	if len(text) > query.MaxTextBytes {
		writeError(w, http.StatusBadRequest, "text is too long")
		return
	}
	// Where the view starts: the last id the browser saw when it reconnects,
	// else "after" (the newest line the page already shows, so nothing falls
	// between its search and the stream), else the newest line stored.
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = q.Get("after")
	}
	batch := h.live.Batch
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > uiMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", uiMaxLimit))
			return
		}
		batch = uint32(n)
	}
	start, err := strconv.ParseInt(cursor, 10, 64)
	if cursor != "" && (err != nil || start <= 0) {
		if r.Header.Get("Last-Event-ID") == "" {
			writeError(w, http.StatusBadRequest, "after must be a time in Unix nanoseconds")
			return
		}
		start = 0 // a mangled id from the browser: start afresh
	}
	select {
	case h.liveSlots <- struct{}{}:
		defer func() { <-h.liveSlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "too many live views are open; try again in a while")
		return
	}

	// The server's WriteTimeout bounds a whole response, which suits a search
	// but would cut this stream off after 90 s. Lift it for this response only;
	// Lifetime bounds it instead.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.live.Lifetime)
	defer cancel()

	t := &tail{h: h, text: text, tags: tags, batch: batch, seen: map[string]int64{}}
	resumed := start > 0
	if resumed {
		t.high = start
	} else {
		st, err := h.client.Stats(ctx, &stratav1.StatsRequest{})
		if err != nil {
			code, msg := httpStatusFor(err)
			writeError(w, code, msg)
			return
		}
		t.high = st.MaxUnixNano
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no") // a buffering proxy would hold events back
	w.WriteHeader(http.StatusOK)

	// The first search only learns what is already there (a resumed stream
	// also sends what arrived after the last line it had), so the view starts
	// with what is new from now on.
	first := true
	for {
		ev, err := t.next(ctx, first, resumed)
		switch {
		case ctx.Err() != nil:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
				send(w, rc, "end", "", map[string]int64{"after_seconds": int64(h.live.Lifetime / time.Second)})
			}
			return
		case err != nil:
			// Busy or slow: say so and try again on the next round. A refused
			// request would be refused every time, so that ends the stream.
			code, msg := httpStatusFor(err)
			retry := code != http.StatusBadRequest && !errors.Is(err, errOldServer)
			if send(w, rc, "problem", "", map[string]any{"error": msg, "retry": retry}) != nil || !retry {
				return
			}
		default:
			first = false
			if send(w, rc, "tick", strconv.FormatInt(t.high, 10), ev) != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(h.live.Every):
		}
	}
}

// tail is one live view's memory: the newest timestamp seen and the lines
// already passed on within the overlap, so the overlap never repeats a line.
type tail struct {
	h     *uiHandler
	text  string
	tags  map[string]string
	batch uint32
	high  int64            // newest timestamp seen, ns
	seen  map[string]int64 // line key -> its timestamp
}

func (t *tail) next(ctx context.Context, first, resumed bool) (*liveEvent, error) {
	overlap := int64(t.h.live.Overlap)
	from := t.high - overlap
	if from < 1 {
		from = 0 // unbounded: nothing stored yet, or timestamps near the epoch
	}
	start := time.Now()
	resp, err := t.h.client.Search(ctx, &stratav1.SearchRequest{
		Text: t.text, Tags: t.tags, FromUnixNano: from, Limit: t.batch, NewestFirst: true,
	})
	if err == nil {
		err = checkOrder(&stratav1.SearchRequest{NewestFirst: true}, resp)
	}
	if err != nil {
		return nil, err
	}
	took := time.Since(start)
	st, err := t.h.client.Stats(ctx, &stratav1.StatsRequest{})
	if err != nil {
		return nil, err
	}

	ev := &liveEvent{Lines: []hitJSON{}, Corpus: shapeCorpus(st, t.h.opts.CorpusName)}
	limit := t.high // on the first search of a resumed stream, lines after this are new
	oldestNew := false
	for _, hit := range resp.Hits { // newest first
		k := lineKey(hit)
		if _, ok := t.seen[k]; ok {
			oldestNew = false
			continue
		}
		t.seen[k] = hit.TimestampUnixNano
		oldestNew = true
		if first && (!resumed || hit.TimestampUnixNano <= limit) {
			continue // already there when the view opened
		}
		ev.Lines = append(ev.Lines, shapeHit(hit))
	}
	for _, hit := range resp.Hits {
		t.high = max(t.high, hit.TimestampUnixNano)
	}
	// The search returns the newest batch lines of the overlap. If it was
	// cut and even its oldest line was new, newer unseen lines may have
	// fallen beyond the cut.
	ev.Gap = !first && resp.Truncated && oldestNew
	for k, ts := range t.seen {
		if ts < t.high-overlap {
			delete(t.seen, k)
		}
	}
	resp.Hits = nil // the lines travel once, in Lines
	ev.Result = shapeSearch(resp, took)
	return ev, nil
}

// lineKey identifies a line by its content, not by the segment holding it:
// compaction moves lines into new segments, and a moved line is not new.
func lineKey(h *stratav1.SearchHit) string {
	return fmt.Sprintf("%d|%s|%v", h.TimestampUnixNano, h.Message, h.Tags) // %v prints map keys sorted
}

// send writes one Server-Sent Event and flushes it to the browser.
func send(w http.ResponseWriter, rc *http.ResponseController, event, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return rc.Flush()
}
