package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var ctx = context.Background()

// readAll decodes every stored segment and returns the entries and the
// number of segments.
func readAll(t *testing.T, s storage.Storage) ([]segment.Entry, int) {
	t.Helper()
	keys, err := s.List(ctx, SegmentPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return readKeys(t, s, keys), len(keys)
}

// readKeys decodes the given segments and returns all their entries.
func readKeys(t *testing.T, s storage.Storage, keys []string) []segment.Entry {
	t.Helper()
	var all []segment.Entry
	for _, k := range keys {
		r, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		seg, err := segment.Decode(data)
		if err != nil {
			t.Fatalf("segment %s: %v", k, err)
		}
		es, err := seg.Entries()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, es...)
	}
	return all
}

func messages(es []segment.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Message
	}
	sort.Strings(out)
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSealsOnSizeThreshold(t *testing.T) {
	s := storagetest.NewMem()
	b, err := NewBuffer(Config{Storage: s, MaxBytes: 500, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)

	// Each entry is ~58 bytes, so nothing is sealed until ~9 are buffered.
	for i := 0; i < 8; i++ {
		if err := b.Add(ctx, segment.Entry{Timestamp: int64(i), Message: fmt.Sprintf("entry number %02d padded padded padded padded", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, n := readAll(t, s); n != 0 {
		t.Fatalf("sealed %d segments before reaching the threshold", n)
	}
	for i := 8; i < 12; i++ {
		if err := b.Add(ctx, segment.Entry{Timestamp: int64(i), Message: fmt.Sprintf("entry number %02d padded padded padded padded", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, n := readAll(t, s); n != 1 {
		t.Fatalf("want 1 segment after crossing threshold, got %d", n)
	}
}

func TestSealsOnAge(t *testing.T) {
	s := storagetest.NewMem()
	b, err := NewBuffer(Config{Storage: s, MaxBytes: 1 << 30, MaxAge: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)

	if err := b.Add(ctx, segment.Entry{Timestamp: 1, Message: "lonely entry"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "age-based seal", func() bool { _, n := readAll(t, s); return n == 1 })
	es, _ := readAll(t, s)
	if len(es) != 1 || es[0].Message != "lonely entry" {
		t.Fatalf("got %+v", es)
	}
}

func TestFlushAndClose(t *testing.T) {
	s := storagetest.NewMem()
	b, _ := NewBuffer(Config{Storage: s, MaxBytes: 1 << 30, MaxAge: time.Hour})

	if err := b.Flush(ctx); err != nil { // empty flush is a no-op
		t.Fatal(err)
	}
	if _, n := readAll(t, s); n != 0 {
		t.Fatal("empty flush wrote a segment")
	}

	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "one"})
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	b.Add(ctx, segment.Entry{Timestamp: 2, Message: "two"})
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	if err := b.Add(ctx, segment.Entry{Message: "late"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
	es, n := readAll(t, s)
	if n != 2 || len(es) != 2 {
		t.Fatalf("want 2 segments/2 entries, got %d/%d", n, len(es))
	}
}

func TestAddCopiesTags(t *testing.T) {
	s := storagetest.NewMem()
	b, _ := NewBuffer(Config{Storage: s, MaxAge: time.Hour})
	tags := map[string]string{"svc": "api"}
	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "x", Tags: tags})
	tags["svc"] = "mutated" // caller reuses its map
	b.Close(ctx)
	es, _ := readAll(t, s)
	if es[0].Tags["svc"] != "api" {
		t.Fatalf("buffer kept caller's map: %v", es[0].Tags)
	}
}

func TestAddRejectsInvalidEntry(t *testing.T) {
	b, _ := NewBuffer(Config{Storage: storagetest.NewMem(), MaxAge: time.Hour})
	defer b.Close(ctx)
	err := b.Add(ctx, segment.Entry{Message: "x", Tags: map[string]string{"a=b": "c"}})
	if !errors.Is(err, segment.ErrInvalidEntry) {
		t.Fatalf("want ErrInvalidEntry, got %v", err)
	}
}

func TestSealedSegmentMetadataAndOnSeal(t *testing.T) {
	s := storagetest.NewMem()
	var got []SealedSegment
	b, _ := NewBuffer(Config{
		Storage: s, MaxAge: time.Hour,
		OnSeal: func(ss SealedSegment) error { got = append(got, ss); return nil },
	})
	b.Add(ctx, segment.Entry{Timestamp: 30, Message: "c"})
	b.Add(ctx, segment.Entry{Timestamp: 10, Message: "a"})
	b.Add(ctx, segment.Entry{Timestamp: 20, Message: "b"})
	b.Close(ctx)

	if len(got) != 1 {
		t.Fatalf("want 1 OnSeal call, got %d", len(got))
	}
	ss := got[0]
	if ss.Count != 3 || ss.MinTS != 10 || ss.MaxTS != 30 || ss.Size <= 0 || ss.ID == "" {
		t.Fatalf("bad metadata: %+v", ss)
	}
	r, err := s.Get(ctx, ss.Key)
	if err != nil {
		t.Fatalf("segment %s not in storage: %v", ss.Key, err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if int64(len(data)) != ss.Size {
		t.Fatalf("size %d does not match stored object %d", ss.Size, len(data))
	}
}

func TestFailedSealLosesNothing(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPuts(2)
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 1 << 30, MaxAge: time.Hour})

	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "a"})
	b.Add(ctx, segment.Entry{Timestamp: 2, Message: "b"})
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected flush to fail")
	}
	// New entries arriving after the failed seal join the requeued ones.
	b.Add(ctx, segment.Entry{Timestamp: 3, Message: "c"})
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected second flush to fail")
	}
	if err := b.Close(ctx); err != nil { // storage has recovered
		t.Fatal(err)
	}
	es, _ := readAll(t, mem)
	if got := messages(es); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("entries after recovery: %v", got)
	}
}

func TestOnSealErrorDeletesSegmentAndKeepsEntries(t *testing.T) {
	s := storagetest.NewMem()
	var fail atomic.Bool
	fail.Store(true)
	b, _ := NewBuffer(Config{
		Storage: s, MaxBytes: 1 << 30, MaxAge: time.Hour,
		OnSeal: func(SealedSegment) error {
			if fail.Load() {
				return errors.New("manifest down")
			}
			return nil
		},
	})
	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "keep me"})
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected error")
	}
	if _, n := readAll(t, s); n != 0 {
		t.Fatalf("unrecorded segment left in storage (%d)", n)
	}
	fail.Store(false)
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	es, n := readAll(t, s)
	if n != 1 || len(es) != 1 || es[0].Message != "keep me" {
		t.Fatalf("entry lost: %d segments, %v", n, es)
	}
}

// recorder is an OnSeal hook that remembers which segments were recorded
// (what the manifest will do in Phase 3). Only recorded segments count as data.
type recorder struct {
	mu   sync.Mutex
	keys []string
	// failFirst makes the first n calls fail.
	failFirst int
}

func (r *recorder) onSeal(ss SealedSegment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFirst > 0 {
		r.failFirst--
		return errors.New("manifest down")
	}
	r.keys = append(r.keys, ss.Key)
	return nil
}

// The upload succeeded but the caller was told it failed (think: a network
// timeout after S3 stored the object). The buffer retries, so the data is
// stored twice, but only the retry was recorded, so each entry is visible
// exactly once. The first copy is an unrecorded orphan.
func TestPutThatStoredButReportedFailureIsNotDuplicated(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPutsAfterStoring(1)
	rec := &recorder{}
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 1 << 30, MaxAge: time.Hour, OnSeal: rec.onSeal})

	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "a"})
	b.Add(ctx, segment.Entry{Timestamp: 2, Message: "b"})
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected the flush to report failure")
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if got := messages(readKeys(t, mem, rec.keys)); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("recorded segments hold %v, want [a b] exactly once", got)
	}
	if _, n := readAll(t, mem); n != 2 {
		t.Fatalf("expected 1 recorded segment + 1 orphan in storage, found %d objects", n)
	}
}

// The manifest rejects a segment, and cleaning up the stored file also fails.
// The file is left behind as an orphan, but nothing is lost and nothing is
// visible twice, because only the retried segment gets recorded.
func TestOnSealFailureWithFailedCleanupLeavesOnlyAnOrphan(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextDeletes(1)
	rec := &recorder{failFirst: 1}
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 1 << 30, MaxAge: time.Hour, OnSeal: rec.onSeal})

	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "a"})
	b.Add(ctx, segment.Entry{Timestamp: 2, Message: "b"})
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected the flush to report failure")
	}
	if _, n := readAll(t, mem); n != 1 {
		t.Fatalf("expected the undeleted orphan to remain, found %d objects", n)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if got := messages(readKeys(t, mem, rec.keys)); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("recorded segments hold %v, want [a b] exactly once", got)
	}
	if _, n := readAll(t, mem); n != 2 {
		t.Fatalf("expected 1 recorded segment + 1 orphan, found %d objects", n)
	}
}

func TestBufferFullWhenSealKeepsFailing(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPuts(1 << 20) // never recovers
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 100, MaxAge: time.Hour})

	var sawFull bool
	for i := 0; i < 1000 && !sawFull; i++ {
		err := b.Add(ctx, segment.Entry{Timestamp: int64(i), Message: "some reasonably long message body"})
		sawFull = errors.Is(err, ErrBufferFull)
	}
	if !sawFull {
		t.Fatal("buffer grew without bound while storage was failing")
	}
}

// TestConcurrentWriters is the stress test: many goroutines write at once
// while both the size and age triggers fire. Afterwards every entry must be
// present exactly once, in a segment that decodes and matches its metadata.
// Run with -race.
func TestConcurrentWriters(t *testing.T) {
	const writers, perWriter = 16, 2000

	s := storagetest.NewMem()
	var (
		mu     sync.Mutex
		sealed []SealedSegment
		errs   []error
	)
	b, err := NewBuffer(Config{
		Storage:  s,
		MaxBytes: 4 << 10, // small: many size-triggered seals
		MaxAge:   2 * time.Millisecond,
		OnSeal: func(ss SealedSegment) error {
			mu.Lock()
			sealed = append(sealed, ss)
			mu.Unlock()
			return nil
		},
		OnError: func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				e := segment.Entry{
					Timestamp: int64(i),
					Message:   fmt.Sprintf("writer%02d-line%05d some log text", w, i),
					Tags:      map[string]string{"writer": fmt.Sprint(w)},
				}
				if err := b.Add(ctx, e); err != nil {
					t.Errorf("add: %v", err)
					return
				}
				if i%500 == 0 {
					time.Sleep(3 * time.Millisecond) // let the age trigger fire mid-run
				}
			}
		}(w)
	}
	wg.Wait()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("background errors: %v", errs)
	}

	es, nSegs := readAll(t, s)
	if len(es) != writers*perWriter {
		t.Fatalf("want %d entries, got %d (lost or duplicated)", writers*perWriter, len(es))
	}
	seen := make(map[string]int, len(es))
	for _, e := range es {
		seen[e.Message]++
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			msg := fmt.Sprintf("writer%02d-line%05d some log text", w, i)
			if seen[msg] != 1 {
				t.Fatalf("%q appears %d times", msg, seen[msg])
			}
		}
	}

	if len(sealed) != nSegs {
		t.Fatalf("OnSeal called %d times for %d stored segments", len(sealed), nSegs)
	}
	total := 0
	for _, ss := range sealed {
		total += ss.Count
	}
	if total != writers*perWriter {
		t.Fatalf("OnSeal counts sum to %d", total)
	}
	t.Logf("%d entries sealed into %d segments", len(es), nSegs)
	if nSegs < 10 {
		t.Fatalf("only %d segments: thresholds were not exercised", nSegs)
	}
}

// TestSealToS3 runs the real ingest path against an S3-compatible server
// (skipped if none is running): many writers, both triggers, then every
// entry must be readable from the bucket exactly once.
func TestSealToS3(t *testing.T) {
	s := storagetest.NewS3(t)
	b, err := NewBuffer(Config{Storage: s, MaxBytes: 8 << 10, MaxAge: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 4, 250
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				e := segment.Entry{Timestamp: int64(i), Message: fmt.Sprintf("w%d-l%04d log text over s3", w, i)}
				if err := b.Add(ctx, e); err != nil {
					t.Errorf("add: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	es, n := readAll(t, s)
	if len(es) != writers*perWriter {
		t.Fatalf("want %d entries, got %d", writers*perWriter, len(es))
	}
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.Message] {
			t.Fatalf("duplicate %q", e.Message)
		}
		seen[e.Message] = true
	}
	t.Logf("%d entries in %d segments on S3", len(es), n)
}

// --- Manifest integration -------------------------------------------------

func toManifestSegment(ss SealedSegment) manifest.Segment {
	return manifest.Segment{ID: ss.ID, Key: ss.Key, MinTS: ss.MinTS, MaxTS: ss.MaxTS, Count: ss.Count, Size: ss.Size}
}

// Sealing a segment records it in the manifest, with metadata that matches
// the stored file.
func TestSealedSegmentsAreRecordedInManifest(t *testing.T) {
	m, err := manifest.Open(filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	mem := storagetest.NewMem()
	b, _ := NewBuffer(Config{
		Storage: mem, MaxBytes: 300, MaxAge: time.Hour,
		OnSeal: func(ss SealedSegment) error { return m.AddSegment(ctx, toManifestSegment(ss)) },
	})
	for i := 0; i < 100; i++ {
		if err := b.Add(ctx, segment.Entry{Timestamp: int64(1000 + i), Message: fmt.Sprintf("line %03d with some padding text", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := m.List(ctx, manifest.StatusActive)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := mem.List(ctx, SegmentPrefix)
	if len(rows) != len(keys) || len(rows) < 2 {
		t.Fatalf("manifest has %d segments, storage has %d", len(rows), len(keys))
	}
	total := 0
	for _, r := range rows {
		total += r.Count
		data, _ := mem.Get(ctx, r.Key)
		raw, _ := io.ReadAll(data)
		seg, err := segment.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(raw)) != r.Size || seg.Len() != r.Count || seg.MinTimestamp() != r.MinTS || seg.MaxTimestamp() != r.MaxTS {
			t.Fatalf("manifest row %+v does not match the stored segment (len %d, count %d, [%d,%d])",
				r, len(raw), seg.Len(), seg.MinTimestamp(), seg.MaxTimestamp())
		}
	}
	if total != 100 {
		t.Fatalf("manifest counts sum to %d, want 100", total)
	}
}

// The manifest becomes unavailable while sealing, then comes back. This is
// the real version of the "OnSeal fails" case: no entry is lost, the stored
// file is cleaned up, and once the manifest is back the entries are recorded
// exactly once.
func TestManifestOutageLosesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.db")
	open := func() *manifest.Manifest {
		m, err := manifest.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	var current atomic.Pointer[manifest.Manifest]
	m := open()
	current.Store(m)

	mem := storagetest.NewMem()
	b, _ := NewBuffer(Config{
		Storage: mem, MaxBytes: 1 << 30, MaxAge: time.Hour,
		OnSeal: func(ss SealedSegment) error {
			return current.Load().AddSegment(ctx, toManifestSegment(ss))
		},
	})
	b.Add(ctx, segment.Entry{Timestamp: 1, Message: "a"})
	b.Add(ctx, segment.Entry{Timestamp: 2, Message: "b"})

	m.Close() // outage
	if err := b.Flush(ctx); err == nil {
		t.Fatal("expected the flush to fail while the manifest is down")
	}
	if _, n := readAll(t, mem); n != 0 {
		t.Fatalf("segment left in storage without a manifest row (%d objects)", n)
	}

	m2 := open() // recovery
	defer m2.Close()
	current.Store(m2)
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := m2.List(ctx, manifest.StatusActive)
	if len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("manifest after recovery: %+v", rows)
	}
	if got := messages(readKeys(t, mem, []string{rows[0].Key})); len(got) != 2 {
		t.Fatalf("recorded segment holds %v", got)
	}
}

// --- AddBatch -------------------------------------------------------------

func TestAddBatchIsAllOrNothing(t *testing.T) {
	mem := storagetest.NewMem()
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 1 << 30, MaxAge: time.Hour})
	err := b.AddBatch(ctx, []segment.Entry{
		{Timestamp: 1, Message: "fine"},
		{Timestamp: 2, Message: "bad", Tags: map[string]string{"a=b": "c"}},
	})
	if !errors.Is(err, segment.ErrInvalidEntry) || Accepted(err) {
		t.Fatalf("want a rejection, got %v", err)
	}
	b.Close(ctx)
	if es, _ := readAll(t, mem); len(es) != 0 {
		t.Fatalf("part of a rejected batch was stored: %v", es)
	}
}

// A seal that fails because of an AddBatch must be reported as a failed seal,
// distinguishable from a rejection, and the batch must still be kept.
func TestAddBatchSealFailureStillAccepts(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPuts(1)
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 50, MaxAge: time.Hour})
	err := b.AddBatch(ctx, []segment.Entry{
		{Timestamp: 1, Message: "this message is long enough to trip the size limit on its own"},
	})
	if !errors.Is(err, ErrSealFailed) || !Accepted(err) {
		t.Fatalf("want ErrSealFailed and Accepted, got %v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if es, _ := readAll(t, mem); len(es) != 1 {
		t.Fatalf("accepted entry was lost: %v", es)
	}
}

func TestAddBatchRejectedWholeWhenFull(t *testing.T) {
	mem := storagetest.NewMem()
	mem.FailNextPuts(1 << 20)
	b, _ := NewBuffer(Config{Storage: mem, MaxBytes: 100, MaxAge: time.Hour})
	var full bool
	for i := 0; i < 100 && !full; i++ {
		err := b.AddBatch(ctx, []segment.Entry{{Message: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {Message: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}})
		full = errors.Is(err, ErrBufferFull)
		if full && Accepted(err) {
			t.Fatal("ErrBufferFull must not count as accepted")
		}
	}
	if !full {
		t.Fatal("never got ErrBufferFull")
	}
}
