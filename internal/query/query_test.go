package query

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/ingest"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var ctx = context.Background()

type env struct {
	t     *testing.T
	path  string
	m     *manifest.Manifest
	store *storagetest.Mem
	eng   *Engine
	all   []segment.Entry // oracle: every entry ever stored
	seq   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.db")
	m, err := manifest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	store := storagetest.NewMem()
	return &env{t: t, path: path, m: m, store: store, eng: New(m, store, 4)}
}

// rawDB opens a second connection to the manifest file, to damage rows in
// ways the manifest API (rightly) doesn't allow.
func (e *env) rawDB() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}

// addSegment stores entries as one segment and records it in the manifest,
// the way ingest does on seal.
func (e *env) addSegment(entries []segment.Entry) manifest.Segment {
	e.t.Helper()
	data, err := segment.Encode(entries)
	if err != nil {
		e.t.Fatal(err)
	}
	seg, err := segment.Decode(data)
	if err != nil {
		e.t.Fatal(err)
	}
	bl, err := segment.ReadBloom(data)
	if err != nil {
		e.t.Fatal(err)
	}
	e.seq++
	row := manifest.Segment{
		ID: fmt.Sprintf("seg%03d", e.seq), Key: fmt.Sprintf("segments/seg%03d.strata", e.seq),
		MinTS: seg.MinTimestamp(), MaxTS: seg.MaxTimestamp(), Count: seg.Len(), Size: int64(len(data)), Bloom: bl,
	}
	if err := e.store.Put(ctx, row.Key, bytes.NewReader(data)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.m.AddSegment(ctx, row); err != nil {
		e.t.Fatal(err)
	}
	e.all = append(e.all, entries...)
	return row
}

// timeSegments creates n segments with disjoint time windows
// [i*1000, i*1000+99], 100 lines each. Every line has "common", the unique
// token "seg<i>", and a "word<k>" token; even lines are tagged env=prod.
func (e *env) timeSegments(n int) []manifest.Segment {
	var rows []manifest.Segment
	for i := 0; i < n; i++ {
		var entries []segment.Entry
		for j := 0; j < 100; j++ {
			en := segment.Entry{Timestamp: int64(i*1000 + j), Message: fmt.Sprintf("common seg%d word%d line%d", i, j%10, j)}
			if j%2 == 0 {
				en.Tags = map[string]string{"env": "prod"}
			}
			entries = append(entries, en)
		}
		rows = append(rows, e.addSegment(entries))
	}
	return rows
}

func (e *env) search(q Query) *Result {
	e.t.Helper()
	res, err := e.eng.Search(ctx, q)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func messages(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Message
	}
	return out
}

// oracle answers a query by brute force over every stored entry.
func (e *env) oracle(q Query) []segment.Entry {
	terms := queryTerms(q)
	var out []segment.Entry
	for _, en := range e.all {
		if q.From != 0 && en.Timestamp < q.From || q.To != 0 && en.Timestamp >= q.To {
			continue
		}
		have := map[string]bool{}
		for _, tok := range segment.Tokenize(en.Message) {
			have[tok] = true
		}
		for k, v := range en.Tags {
			have[segment.TagTerm(k, v)] = true
		}
		ok := true
		for _, t := range terms {
			if !have[t] {
				ok = false
			}
		}
		if ok {
			out = append(out, en)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

// ---------------------------------------------------------------------------

// The headline test: a narrow time range reads one segment out of ten, and the
// metrics prove it.
func TestNarrowTimeRangeSkipsSegments(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(10)
	var total int64
	for _, r := range rows {
		total += r.Size
	}

	res := e.search(Query{Text: "common", From: 3000, To: 4000})
	m := res.Metrics
	t.Log(m)
	if len(res.Hits) != 100 {
		t.Fatalf("want 100 hits, got %d", len(res.Hits))
	}
	if m.SegmentsConsidered != 10 || m.SkippedByTime != 9 || m.SegmentsScanned != 1 {
		t.Fatalf("metrics: %+v", m)
	}
	if m.BytesRead != rows[3].Size {
		t.Fatalf("read %d bytes, want exactly segment 3 (%d)", m.BytesRead, rows[3].Size)
	}
	if m.BytesRead*5 > total {
		t.Fatalf("read %d of %d bytes: not a meaningful saving", m.BytesRead, total)
	}
	for _, h := range res.Hits {
		if h.SegmentID != rows[3].ID {
			t.Fatalf("hit from unexpected segment %s", h.SegmentID)
		}
	}
}

// A term that exists in one segment only: the time filter can't help (no
// range given), the bloom filters do.
func TestBloomSkipsSegmentsWithoutTheTerm(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(10)

	res := e.search(Query{Text: "seg7"})
	m := res.Metrics
	t.Log(m)
	if len(res.Hits) != 100 {
		t.Fatalf("want 100 hits, got %d", len(res.Hits))
	}
	if m.SkippedByTime != 0 {
		t.Fatalf("no time range was given, yet %d skipped by time", m.SkippedByTime)
	}
	// The filter has a ~1% false positive rate, so allow a stray extra read,
	// but it must have skipped nearly everything.
	if m.SegmentsScanned < 1 || m.SegmentsScanned > 2 || m.SkippedByBloom < 8 {
		t.Fatalf("metrics: %+v", m)
	}
	if res.Hits[0].SegmentID != rows[7].ID {
		t.Fatalf("wrong segment %s", res.Hits[0].SegmentID)
	}

	// A term that exists nowhere reads (almost) nothing.
	res = e.search(Query{Text: "nonexistentterm"})
	if len(res.Hits) != 0 || res.Metrics.SegmentsScanned > 1 {
		t.Fatalf("absent term: hits=%d metrics=%+v", len(res.Hits), res.Metrics)
	}
}

func TestTimeAndTermFiltersCombine(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(10)
	// Term exists only in segment 7, time range only covers segment 2: both
	// filters apply and nothing needs reading.
	res := e.search(Query{Text: "seg7", From: 2000, To: 3000})
	if len(res.Hits) != 0 || res.Metrics.SegmentsScanned != 0 {
		t.Fatalf("hits=%d metrics=%+v", len(res.Hits), res.Metrics)
	}
}

func TestTagsAreExactMatch(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(3)
	res := e.search(Query{Text: "seg1", Tags: map[string]string{"env": "prod"}})
	if len(res.Hits) != 50 {
		t.Fatalf("want 50 prod lines in seg1, got %d", len(res.Hits))
	}
	for _, h := range res.Hits {
		if h.Tags["env"] != "prod" {
			t.Fatalf("hit without the tag: %+v", h)
		}
	}
	if res := e.search(Query{Tags: map[string]string{"env": "staging"}}); len(res.Hits) != 0 {
		t.Fatalf("tag value that doesn't exist matched %d lines", len(res.Hits))
	}
}

func TestAllTermsMustMatchInTheSameLine(t *testing.T) {
	e := newEnv(t)
	e.addSegment([]segment.Entry{
		{Timestamp: 1, Message: "disk full on node a"},
		{Timestamp: 2, Message: "disk ok on node b"},
		{Timestamp: 3, Message: "memory full on node b"},
	})
	got := messages(e.search(Query{Text: "FULL disk"}).Hits) // case and order don't matter
	if !reflect.DeepEqual(got, []string{"disk full on node a"}) {
		t.Fatalf("got %v", got)
	}
}

// Segments written by concurrent ingest overlap in time. Results must still
// come back globally ordered by timestamp.
func TestResultsAreMergedAcrossOverlappingSegments(t *testing.T) {
	e := newEnv(t)
	e.addSegment([]segment.Entry{{Timestamp: 10, Message: "x a"}, {Timestamp: 30, Message: "x c"}, {Timestamp: 50, Message: "x e"}})
	e.addSegment([]segment.Entry{{Timestamp: 20, Message: "x b"}, {Timestamp: 40, Message: "x d"}, {Timestamp: 60, Message: "x f"}})
	e.addSegment([]segment.Entry{{Timestamp: 5, Message: "x z"}, {Timestamp: 35, Message: "x m"}})

	res := e.search(Query{Text: "x"})
	var ts []int64
	for _, h := range res.Hits {
		ts = append(ts, h.Timestamp)
	}
	if want := []int64{5, 10, 20, 30, 35, 40, 50, 60}; !reflect.DeepEqual(ts, want) {
		t.Fatalf("timestamps %v, want %v", ts, want)
	}
}

func TestLimitAndTruncation(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(5)
	res := e.search(Query{Text: "common", Limit: 120})
	if len(res.Hits) != 120 || !res.Truncated {
		t.Fatalf("hits=%d truncated=%v", len(res.Hits), res.Truncated)
	}
	// The earliest 120 matches: all of segment 0 and 20 of segment 1.
	want := e.oracle(Query{Text: "common"})[:120]
	for i, h := range res.Hits {
		if h.Timestamp != want[i].Timestamp {
			t.Fatalf("hit %d: ts %d, want %d", i, h.Timestamp, want[i].Timestamp)
		}
	}
	if res := e.search(Query{Text: "common", Limit: 500}); res.Truncated || len(res.Hits) != 500 {
		t.Fatalf("exactly-at-limit must not be truncated: hits=%d truncated=%v", len(res.Hits), res.Truncated)
	}
}

// Regression test: when a single segment alone holds more matches than the
// limit, the result must still be marked truncated.
func TestTruncationIsReportedForASingleSegment(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(1) // 100 matching lines in one segment
	res := e.search(Query{Text: "common", Limit: 10})
	if len(res.Hits) != 10 || !res.Truncated {
		t.Fatalf("hits=%d truncated=%v, want 10 hits and truncated", len(res.Hits), res.Truncated)
	}
	if res := e.search(Query{Text: "common", Limit: 100}); res.Truncated || len(res.Hits) != 100 {
		t.Fatalf("a limit equal to the match count must not be truncated: hits=%d truncated=%v", len(res.Hits), res.Truncated)
	}
}

func TestEmptyManifest(t *testing.T) {
	e := newEnv(t)
	res := e.search(Query{Text: "anything"})
	if len(res.Hits) != 0 || res.Metrics.SegmentsConsidered != 0 {
		t.Fatalf("%+v", res)
	}
}

// Whatever the filters skip, the answer must equal a brute-force scan of all
// the data. Random corpus with overlapping segments, random queries.
func TestAgreesWithBruteForce(t *testing.T) {
	e := newEnv(t)
	rng := rand.New(rand.NewSource(7))
	words := []string{"alpha", "beta", "gamma", "delta", "error", "timeout", "ok", "db", "cache", "retry"}
	for s := 0; s < 12; s++ {
		var entries []segment.Entry
		base := int64(rng.Intn(500))
		for i := 0; i < 80+rng.Intn(80); i++ {
			var parts []string
			for j, n := 0, 1+rng.Intn(4); j < n; j++ {
				parts = append(parts, words[rng.Intn(len(words))])
			}
			en := segment.Entry{Timestamp: base + int64(rng.Intn(400)), Message: strings.Join(parts, " ")}
			if rng.Intn(3) == 0 {
				en.Tags = map[string]string{"svc": fmt.Sprintf("s%d", rng.Intn(3))}
			}
			entries = append(entries, en)
		}
		e.addSegment(entries)
	}
	for i := 0; i < 300; i++ {
		q := Query{Limit: 100000}
		for j, n := 0, rng.Intn(3); j < n; j++ {
			q.Text += words[rng.Intn(len(words))] + " "
		}
		if rng.Intn(4) == 0 {
			q.Tags = map[string]string{"svc": fmt.Sprintf("s%d", rng.Intn(3))}
		}
		if rng.Intn(2) == 0 {
			q.From = int64(rng.Intn(900))
		}
		if rng.Intn(2) == 0 {
			q.To = q.From + int64(1+rng.Intn(400))
		}
		got := e.search(q)
		want := e.oracle(q)
		if len(got.Hits) != len(want) {
			t.Fatalf("query %+v: got %d hits, want %d", q, len(got.Hits), len(want))
		}
		// Both must be in timestamp order. Lines with the same timestamp may
		// legitimately come in any order, so compare them as sets.
		for k := 1; k < len(got.Hits); k++ {
			if got.Hits[k].Timestamp < got.Hits[k-1].Timestamp {
				t.Fatalf("query %+v: hits not in timestamp order at %d", q, k)
			}
		}
		key := func(ts int64, msg string, tags map[string]string) string {
			return fmt.Sprintf("%d|%s|%v", ts, msg, tags)
		}
		var g, w []string
		for _, h := range got.Hits {
			g = append(g, key(h.Timestamp, h.Message, h.Tags))
		}
		for _, en := range want {
			w = append(w, key(en.Timestamp, en.Message, en.Tags))
		}
		sort.Strings(g)
		sort.Strings(w)
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("query %+v: results differ from brute force", q)
		}
	}
}

// ---- failure cases --------------------------------------------------------

// A segment written before blooms were stored (NULL) can't be ruled out, but
// must still be searched correctly.
func TestSegmentWithoutBloomIsAlwaysScanned(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(3)
	if _, err := e.rawDB().ExecContext(ctx, `UPDATE segments SET bloom = NULL WHERE id = ?`, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	res := e.search(Query{Text: "seg0"}) // only segment 0 has it
	if len(res.Hits) != 100 {
		t.Fatalf("hits=%d", len(res.Hits))
	}
	if res.Metrics.SegmentsScanned < 2 { // segment 0, plus segment 1 that can't be skipped
		t.Fatalf("segment without a bloom filter was skipped: %+v", res.Metrics)
	}
}

// A damaged bloom filter in the manifest must fall back to reading the
// segment, not to skipping it: the safe direction.
func TestCorruptBloomFallsBackToScanning(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(2)
	if _, err := e.rawDB().ExecContext(ctx, `UPDATE segments SET bloom = x'00ff' WHERE id = ?`, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	res := e.search(Query{Text: "seg1"})
	if len(res.Hits) != 100 {
		t.Fatalf("hits=%d", len(res.Hits))
	}
}

// A segment that is damaged in storage must fail the query loudly, naming the
// segment, rather than returning an incomplete answer that looks complete.
func TestCorruptSegmentFailsTheQuery(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(3)
	data := readAll(t, e.store, rows[1].Key)
	data[len(data)/2] ^= 0xFF
	e.store.Put(ctx, rows[1].Key, bytes.NewReader(data))

	_, err := e.eng.Search(ctx, Query{Text: "common"})
	if !errors.Is(err, segment.ErrCorrupt) || !strings.Contains(err.Error(), rows[1].ID) {
		t.Fatalf("want ErrCorrupt naming %s, got %v", rows[1].ID, err)
	}
	// A query that doesn't touch the damaged segment is unaffected.
	if _, err := e.eng.Search(ctx, Query{Text: "common", From: 0, To: 1000}); err != nil {
		t.Fatalf("query avoiding the damaged segment failed: %v", err)
	}
}

// racingStore simulates a compaction landing between the query reading the
// manifest and fetching a segment: on the first Get it commits the swap (merged
// segment recorded, originals marked deleted) and removes the originals' files.
type racingStore struct {
	storage.Storage
	swap  func()
	fired atomic.Bool
}

func (r *racingStore) Get(c context.Context, key string) (io.ReadCloser, error) {
	if r.fired.CompareAndSwap(false, true) {
		r.swap()
	}
	return r.Storage.Get(c, key)
}

func TestQueryRestartsWhenSegmentIsReplacedMidQuery(t *testing.T) {
	e := newEnv(t)
	a := e.addSegment([]segment.Entry{{Timestamp: 1, Message: "x one"}, {Timestamp: 2, Message: "x two"}})
	b := e.addSegment([]segment.Entry{{Timestamp: 3, Message: "x three"}})

	// Prepare the merged segment's file up front; the swap just commits it.
	merged := []segment.Entry{{Timestamp: 1, Message: "x one"}, {Timestamp: 2, Message: "x two"}, {Timestamp: 3, Message: "x three"}}
	data, _ := segment.Encode(merged)
	bl, _ := segment.ReadBloom(data)
	mrow := manifest.Segment{ID: "merged", Key: "segments/merged.strata", MinTS: 1, MaxTS: 3, Count: 3, Size: int64(len(data)), Bloom: bl}
	e.store.Put(ctx, mrow.Key, bytes.NewReader(data))

	rs := &racingStore{Storage: e.store}
	rs.swap = func() {
		if err := e.m.Replace(ctx, []manifest.Segment{mrow}, []string{a.ID, b.ID}); err != nil {
			t.Error(err)
		}
		e.store.Delete(ctx, a.Key) // cleanup after the commit, as compaction will do
		e.store.Delete(ctx, b.Key)
	}
	eng := New(e.m, rs, 1)

	res, err := eng.Search(ctx, Query{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(res.Hits); !reflect.DeepEqual(got, []string{"x one", "x two", "x three"}) {
		t.Fatalf("got %v", got)
	}
	if res.Metrics.Retries != 1 {
		t.Fatalf("want exactly 1 retry, got %d", res.Metrics.Retries)
	}
}

// A segment that is listed but really gone (not a race) must end in an
// error, not loop forever.
func TestMissingSegmentEventuallyErrors(t *testing.T) {
	e := newEnv(t)
	rows := e.timeSegments(2)
	e.store.Delete(ctx, rows[0].Key)
	_, err := e.eng.Search(ctx, Query{Text: "common"})
	if !errors.Is(err, ErrSegmentMissing) {
		t.Fatalf("want a missing-segment error, got %v", err)
	}
}

func TestStorageErrorFailsTheQuery(t *testing.T) {
	e := newEnv(t)
	e.timeSegments(2)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.eng.Search(cctx, Query{Text: "common"}); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

// Full path: lines go through the ingest buffer, which seals segments and
// records them (with their blooms) in the manifest; the query engine then
// finds them.
func TestEndToEndThroughIngest(t *testing.T) {
	e := newEnv(t)
	buf, err := ingest.NewBuffer(ingest.Config{
		Storage: e.store, MaxBytes: 2000, MaxAge: time.Hour,
		OnSeal: func(ss ingest.SealedSegment) error {
			return e.m.AddSegment(ctx, manifest.Segment{ID: ss.ID, Key: ss.Key, MinTS: ss.MinTS, MaxTS: ss.MaxTS, Count: ss.Count, Size: ss.Size, Bloom: ss.Bloom})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		msg := fmt.Sprintf("request %d handled", i)
		if i%100 == 7 {
			msg += " ERROR upstream timeout"
		}
		if err := buf.Add(ctx, segment.Entry{Timestamp: int64(i), Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := buf.Close(ctx); err != nil {
		t.Fatal(err)
	}

	res := e.search(Query{Text: "error timeout"})
	t.Log(res.Metrics)
	if len(res.Hits) != 10 {
		t.Fatalf("want 10 error lines, got %d", len(res.Hits))
	}
	if res.Metrics.SegmentsConsidered < 5 || res.Metrics.SegmentsScanned >= res.Metrics.SegmentsConsidered {
		t.Fatalf("expected the bloom filters to skip segments: %+v", res.Metrics)
	}
	// Narrow time window: only the segment(s) covering it are read.
	res = e.search(Query{Text: "request", From: 500, To: 520})
	if len(res.Hits) != 20 || res.Metrics.SkippedByTime == 0 {
		t.Fatalf("hits=%d metrics=%+v", len(res.Hits), res.Metrics)
	}
}

func readAll(t *testing.T, s storage.Storage, key string) []byte {
	t.Helper()
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
