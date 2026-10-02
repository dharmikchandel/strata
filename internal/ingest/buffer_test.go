package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
)

var ctx = context.Background()

func newLocal(t *testing.T) *storage.Local {
	t.Helper()
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// readAll decodes every stored segment and returns the entries and the
// number of segments.
func readAll(t *testing.T, s storage.Storage) ([]segment.Entry, int) {
	t.Helper()
	keys, err := s.List(ctx, SegmentPrefix)
	if err != nil {
		t.Fatal(err)
	}
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
	return all, len(keys)
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
	s := newLocal(t)
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
	s := newLocal(t)
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
	s := newLocal(t)
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
	s := newLocal(t)
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
	b, _ := NewBuffer(Config{Storage: newLocal(t), MaxAge: time.Hour})
	defer b.Close(ctx)
	err := b.Add(ctx, segment.Entry{Message: "x", Tags: map[string]string{"a=b": "c"}})
	if !errors.Is(err, segment.ErrInvalidEntry) {
		t.Fatalf("want ErrInvalidEntry, got %v", err)
	}
}

func TestSealedSegmentMetadataAndOnSeal(t *testing.T) {
	s := newLocal(t)
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

// flakyStorage fails the first n Puts, then delegates.
type flakyStorage struct {
	storage.Storage
	failures atomic.Int32
}

func (f *flakyStorage) Put(c context.Context, key string, r io.Reader) error {
	if f.failures.Add(-1) >= 0 {
		return errors.New("disk on fire")
	}
	return f.Storage.Put(c, key, r)
}

func TestFailedSealLosesNothing(t *testing.T) {
	fs := &flakyStorage{Storage: newLocal(t)}
	fs.failures.Store(2)
	b, _ := NewBuffer(Config{Storage: fs, MaxBytes: 1 << 30, MaxAge: time.Hour})

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
	es, _ := readAll(t, fs.Storage)
	if got := messages(es); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("entries after recovery: %v", got)
	}
}

func TestOnSealErrorDeletesSegmentAndKeepsEntries(t *testing.T) {
	s := newLocal(t)
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

func TestBufferFullWhenSealKeepsFailing(t *testing.T) {
	fs := &flakyStorage{Storage: newLocal(t)}
	fs.failures.Store(1 << 20) // never recovers
	b, _ := NewBuffer(Config{Storage: fs, MaxBytes: 100, MaxAge: time.Hour})

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

	s := newLocal(t)
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
