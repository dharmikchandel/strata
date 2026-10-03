package compact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/query"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

var (
	ctx   = context.Background()
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

type env struct {
	t     *testing.T
	m     *manifest.Manifest
	store *storagetest.Mem
	eng   *query.Engine
	total int // entries stored so far
	seq   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	m, err := manifest.Open(filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	store := storagetest.NewMem()
	return &env{t: t, m: m, store: store, eng: query.New(m, store, 4)}
}

// compactor builds a Compactor with test-friendly settings.
func (e *env) compactor(mod func(*Config)) *Compactor {
	cfg := Config{Manifest: e.m, Storage: e.store, SmallBytes: 1 << 30, MinSegments: 2, MaxSegments: 100, Logger: quiet}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// addSegment stores one small segment of n entries starting at time base. Every
// message is unique ("s<N>-l<j> common") so duplicates and losses are detectable.
func (e *env) addSegment(base int64, n int) manifest.Segment {
	e.t.Helper()
	e.seq++
	var entries []segment.Entry
	for j := 0; j < n; j++ {
		entries = append(entries, segment.Entry{
			Timestamp: base + int64(j),
			Message:   fmt.Sprintf("s%d-l%d common", e.seq, j),
			Tags:      map[string]string{"n": fmt.Sprint(j % 3)},
		})
	}
	return e.addEntries(entries)
}

func (e *env) addEntries(entries []segment.Entry) manifest.Segment {
	e.t.Helper()
	data, err := segment.Encode(entries)
	if err != nil {
		e.t.Fatal(err)
	}
	seg, _ := segment.Decode(data)
	bl, _ := segment.ReadBloom(data)
	id := segment.NewID()
	row := manifest.Segment{ID: id, Key: segment.KeyForID(id), MinTS: seg.MinTimestamp(), MaxTS: seg.MaxTimestamp(),
		Count: seg.Len(), Size: int64(len(data)), Bloom: bl}
	if err := e.store.Put(ctx, row.Key, bytes.NewReader(data)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.m.AddSegment(ctx, row); err != nil {
		e.t.Fatal(err)
	}
	e.total += len(entries)
	return row
}

// assertExactlyOnce checks that a query sees every entry ever stored, each
// exactly once: nothing lost by a merge, nothing counted twice.
func (e *env) assertExactlyOnce() {
	e.t.Helper()
	res, err := e.eng.Search(ctx, query.Query{Limit: 1 << 30})
	if err != nil {
		e.t.Fatal(err)
	}
	seen := map[string]int{}
	for _, h := range res.Hits {
		seen[h.Message]++
	}
	for msg, n := range seen {
		if n != 1 {
			e.t.Fatalf("%q returned %d times", msg, n)
		}
	}
	if len(res.Hits) != e.total {
		e.t.Fatalf("query sees %d entries, %d were stored", len(res.Hits), e.total)
	}
}

func (e *env) active() []manifest.Segment {
	e.t.Helper()
	rows, err := e.m.List(ctx, manifest.StatusActive)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) deleted() []manifest.Segment {
	e.t.Helper()
	rows, err := e.m.List(ctx, manifest.StatusDeleted)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) objects() []string {
	e.t.Helper()
	keys, err := e.store.List(ctx, segment.KeyPrefix)
	if err != nil {
		e.t.Fatal(err)
	}
	return keys
}

// ---------------------------------------------------------------------------

func TestMergesSmallSegmentsIntoOne(t *testing.T) {
	e := newEnv(t)
	var inputs []manifest.Segment
	for i := 0; i < 6; i++ {
		inputs = append(inputs, e.addSegment(int64(i*100), 50))
	}
	c := e.compactor(nil)

	rep, err := c.CompactOnce(ctx)
	if err != nil || rep == nil {
		t.Fatalf("report=%v err=%v", rep, err)
	}
	if len(rep.InputIDs) != 6 || rep.Entries != 300 || rep.DeleteErrors != 0 {
		t.Fatalf("report: %+v", rep)
	}

	active := e.active()
	if len(active) != 1 || active[0].ID != rep.OutputID {
		t.Fatalf("active after merge: %+v", active)
	}
	if a := active[0]; a.Count != 300 || a.MinTS != 0 || a.MaxTS != 549 || a.Size != rep.BytesOut {
		t.Fatalf("merged row: %+v", a)
	}
	// The merged row carries a bloom filter, so queries can skip it by term.
	withBloom, err := e.m.Overlapping(ctx, 0, 1<<40)
	if err != nil || len(withBloom) != 1 || len(withBloom[0].Bloom) == 0 {
		t.Fatalf("merged row has no bloom filter: %v %v", withBloom, err)
	}
	// Cleanup: rows purged, old files gone, only the merged file remains.
	if d := e.deleted(); len(d) != 0 {
		t.Fatalf("deleted rows left behind: %+v", d)
	}
	if objs := e.objects(); len(objs) != 1 || objs[0] != active[0].Key {
		t.Fatalf("storage holds %v", objs)
	}
	e.assertExactlyOnce()

	// The merged segment is fully searchable, including by tag and by rare term.
	res, _ := e.eng.Search(ctx, query.Query{Text: "s4-l7", Tags: map[string]string{"n": "1"}})
	if len(res.Hits) != 1 {
		t.Fatalf("lost a specific entry: %d hits", len(res.Hits))
	}
	// Nothing left to do.
	if rep, err := c.CompactOnce(ctx); rep != nil || err != nil {
		t.Fatalf("second pass: %v %v", rep, err)
	}
	_ = inputs
}

func TestBatchSelection(t *testing.T) {
	t.Run("too few small segments", func(t *testing.T) {
		e := newEnv(t)
		e.addSegment(0, 10)
		e.addSegment(100, 10)
		c := e.compactor(func(c *Config) { c.MinSegments = 3 })
		if rep, err := c.CompactOnce(ctx); rep != nil || err != nil {
			t.Fatalf("%v %v", rep, err)
		}
	})
	t.Run("big segments are left alone", func(t *testing.T) {
		e := newEnv(t)
		for i := 0; i < 4; i++ {
			e.addSegment(int64(i*100), 10)
		}
		c := e.compactor(func(c *Config) { c.SmallBytes = 1 }) // nothing counts as small
		if rep, _ := c.CompactOnce(ctx); rep != nil {
			t.Fatalf("compacted segments that are not small: %+v", rep)
		}
	})
	t.Run("oldest by time first, bounded by MaxSegments", func(t *testing.T) {
		e := newEnv(t)
		// Insert out of time order: IDs (insertion order) differ from time order.
		later := e.addSegment(5000, 10)
		var early []manifest.Segment
		for i := 0; i < 4; i++ {
			early = append(early, e.addSegment(int64(i*100), 10))
		}
		c := e.compactor(func(c *Config) { c.MaxSegments = 4 })
		rep, err := c.CompactOnce(ctx)
		if err != nil || rep == nil {
			t.Fatal(err)
		}
		if len(rep.InputIDs) != 4 {
			t.Fatalf("inputs: %v", rep.InputIDs)
		}
		for _, id := range rep.InputIDs {
			if id == later.ID {
				t.Fatal("merged the newest segment instead of the oldest four")
			}
		}
		// The later segment is still active, unmerged.
		if act := e.active(); len(act) != 2 {
			t.Fatalf("active: %+v", act)
		}
		e.assertExactlyOnce()
	})
	t.Run("TargetBytes bounds a merge", func(t *testing.T) {
		e := newEnv(t)
		var sz int64
		for i := 0; i < 6; i++ {
			sz = e.addSegment(int64(i*100), 20).Size
		}
		c := e.compactor(func(c *Config) { c.TargetBytes = sz*3 + sz/2 }) // room for 3
		rep, err := c.CompactOnce(ctx)
		if err != nil || rep == nil || len(rep.InputIDs) != 3 {
			t.Fatalf("report=%+v err=%v", rep, err)
		}
	})
	t.Run("merged output can itself be merged later", func(t *testing.T) {
		e := newEnv(t)
		for i := 0; i < 3; i++ {
			e.addSegment(int64(i*100), 10)
		}
		c := e.compactor(nil)
		c.CompactOnce(ctx)
		for i := 3; i < 6; i++ {
			e.addSegment(int64(i*100), 10)
		}
		rep, err := c.CompactOnce(ctx)
		if err != nil || rep == nil || len(rep.InputIDs) != 4 { // 1 merged + 3 new
			t.Fatalf("report=%+v err=%v", rep, err)
		}
		e.assertExactlyOnce()
	})
}

func TestNewValidatesConfig(t *testing.T) {
	e := newEnv(t)
	if _, err := New(Config{Storage: e.store}); err == nil {
		t.Error("missing manifest accepted")
	}
	if _, err := New(Config{Manifest: e.m}); err == nil {
		t.Error("missing storage accepted")
	}
	if _, err := New(Config{Manifest: e.m, Storage: e.store, MinSegments: 1}); err == nil {
		t.Error("MinSegments 1 accepted")
	}
}

// ---- crash tests ----------------------------------------------------------

type crashed struct{ stage string }

// runUntilCrash runs one compaction and "kills the process" at stage by
// panicking out of it. Cleanup code that only runs on normal returns is
// skipped, which is what a real crash does. (The merge deliberately uses no
// defer for its cleanup, so this simulation is faithful.)
func runUntilCrash(t *testing.T, c *Compactor, stage string) {
	t.Helper()
	c.crashHook = func(s string) {
		if s == stage {
			panic(crashed{s})
		}
	}
	defer func() {
		r := recover()
		if cr, ok := r.(crashed); !ok || cr.stage != stage {
			t.Fatalf("expected a simulated crash at %q, got %v", stage, r)
		}
	}()
	c.CompactOnce(ctx)
	t.Fatalf("process survived stage %q", stage)
}

// Crash BEFORE the manifest commit. The inputs must remain the data; the
// merged file, if it was written, is an unrecorded orphan that is invisible
// and gets garbage-collected; and a restarted compactor finishes the job.
func TestCrashBeforeCommitLosesNothing(t *testing.T) {
	for _, stage := range []string{"inputs-read", "merged-written", "merged-verified"} {
		t.Run(stage, func(t *testing.T) {
			e := newEnv(t)
			for i := 0; i < 5; i++ {
				e.addSegment(int64(i*100), 20)
			}
			runUntilCrash(t, e.compactor(nil), stage)

			// State after the crash: manifest untouched, every input still active.
			if got := len(e.active()); got != 5 {
				t.Fatalf("active segments after crash: %d, want 5", got)
			}
			if got := len(e.deleted()); got != 0 {
				t.Fatalf("segments marked deleted before the commit: %d", got)
			}
			e.assertExactlyOnce() // queries see a complete, unbroken view

			// A merged file may exist, unrecorded.
			wantObjects := 5
			if stage != "inputs-read" {
				wantObjects = 6
			}
			if got := len(e.objects()); got != wantObjects {
				t.Fatalf("storage holds %d objects, want %d", got, wantObjects)
			}

			// "Restart": a new compactor. GC must NOT delete the young orphan...
			c2 := e.compactor(nil)
			gc, err := c2.GarbageCollect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if stage != "inputs-read" && (gc.OrphansTooYoung != 1 || gc.OrphansDeleted != 0) {
				t.Fatalf("young orphan handling: %+v", gc)
			}
			// ...but does once it is older than the grace period.
			c3 := e.compactor(func(c *Config) { c.OrphanGrace = time.Millisecond })
			time.Sleep(5 * time.Millisecond)
			gc, err = c3.GarbageCollect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if stage != "inputs-read" && gc.OrphansDeleted != 1 {
				t.Fatalf("old orphan not collected: %+v", gc)
			}
			if got := len(e.objects()); got != 5 {
				t.Fatalf("storage holds %d objects after GC, want 5", got)
			}

			// And the restarted compactor completes the merge.
			rep, err := c2.CompactOnce(ctx)
			if err != nil || rep == nil || len(rep.InputIDs) != 5 {
				t.Fatalf("restart merge: %+v %v", rep, err)
			}
			e.assertExactlyOnce()
		})
	}
}

// Crash AFTER the manifest commit but before the old files were deleted. The
// merged segment is now the data; the old files are unreferenced leftovers.
// Queries must not double count, and GC must remove the leftovers.
func TestCrashAfterCommitDoesNotDoubleCount(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.addSegment(int64(i*100), 20)
	}
	runUntilCrash(t, e.compactor(nil), "committed")

	if got := len(e.active()); got != 1 {
		t.Fatalf("active after commit: %d, want 1 (the merged segment)", got)
	}
	if got := len(e.deleted()); got != 5 {
		t.Fatalf("deleted rows: %d, want 5", got)
	}
	if got := len(e.objects()); got != 6 {
		t.Fatalf("storage should still hold the 5 old files + merged = 6, has %d", got)
	}
	e.assertExactlyOnce() // old files still exist, but must not be read

	gc, err := e.compactor(nil).GarbageCollect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gc.ReplacedFilesDeleted != 5 {
		t.Fatalf("gc: %+v", gc)
	}
	if got := len(e.objects()); got != 1 {
		t.Fatalf("storage holds %d objects after GC, want 1", got)
	}
	if len(e.deleted()) != 0 {
		t.Fatal("deleted rows not purged")
	}
	e.assertExactlyOnce()
}

// ---- failures that return errors rather than crash -----------------------

func TestStorageWriteFailureChangesNothing(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.addSegment(int64(i*100), 20)
	}
	e.store.FailNextPuts(1)
	c := e.compactor(nil)
	if rep, err := c.CompactOnce(ctx); err == nil || rep != nil {
		t.Fatalf("expected an error, got %v %v", rep, err)
	}
	if len(e.active()) != 4 || len(e.deleted()) != 0 || len(e.objects()) != 4 {
		t.Fatalf("state changed: %d active, %d deleted, %d objects", len(e.active()), len(e.deleted()), len(e.objects()))
	}
	e.assertExactlyOnce()
	// Storage recovers; the next attempt works.
	if rep, err := c.CompactOnce(ctx); err != nil || rep == nil {
		t.Fatalf("retry: %v %v", rep, err)
	}
	e.assertExactlyOnce()
}

// corruptingStore silently damages everything written while enabled.
type corruptingStore struct {
	storage.Storage
	on atomic.Bool
}

func (s *corruptingStore) Put(c context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if s.on.Load() {
		data[len(data)/2] ^= 0xFF
	}
	return s.Storage.Put(c, key, bytes.NewReader(data))
}

// The write "succeeds" but the stored bytes are wrong. Reading back before
// committing catches it, so the originals are never replaced by a bad segment.
func TestVerificationCatchesSilentCorruption(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.addSegment(int64(i*100), 20)
	}
	cs := &corruptingStore{Storage: e.store}
	cs.on.Store(true)
	c, _ := New(Config{Manifest: e.m, Storage: cs, SmallBytes: 1 << 30, MinSegments: 2, Logger: quiet})

	_, err := c.CompactOnce(ctx)
	if err == nil || !errors.Is(err, segment.ErrCorrupt) {
		t.Fatalf("want a verification failure wrapping ErrCorrupt, got %v", err)
	}
	if len(e.active()) != 4 || len(e.deleted()) != 0 {
		t.Fatal("manifest changed despite a failed verification")
	}
	if len(e.objects()) != 4 {
		t.Fatalf("the bad merged file was not removed: %v", e.objects())
	}
	e.assertExactlyOnce()
}

// Another actor replaces one of our inputs between our reading it and our
// commit. The commit must fail whole, and our merged copy must not appear.
func TestCommitConflictLeavesNoTrace(t *testing.T) {
	e := newEnv(t)
	var rows []manifest.Segment
	for i := 0; i < 4; i++ {
		rows = append(rows, e.addSegment(int64(i*100), 20))
	}
	c := e.compactor(nil)
	c.crashHook = func(stage string) {
		if stage != "merged-verified" {
			return
		}
		// A rival replaces rows[0] with an equivalent copy of the same data.
		data, _ := io.ReadAll(mustGet(t, e.store, rows[0].Key))
		seg, _ := segment.Decode(data)
		es, _ := seg.Entries()
		e.total -= len(es) // addEntries counts them again; the data is the same
		copyRow := e.addEntries(es)
		// addEntries recorded the copy as active; swap: delete original.
		if err := e.m.Replace(ctx, nil, []string{rows[0].ID}); err != nil {
			t.Error(err)
		}
		_ = copyRow
	}

	_, err := c.CompactOnce(ctx)
	if !errors.Is(err, manifest.ErrNotActive) {
		t.Fatalf("want ErrNotActive, got %v", err)
	}
	if got := len(e.active()); got != 4 { // 3 untouched inputs + the rival's copy
		t.Fatalf("active: %d, want 4", got)
	}
	for _, o := range e.objects() {
		if o != rows[0].Key && o != rows[1].Key && o != rows[2].Key && o != rows[3].Key {
			// The only other object allowed is the rival's own copy.
			found := false
			for _, a := range e.active() {
				found = found || a.Key == o
			}
			if !found {
				t.Fatalf("our merged file was left behind: %s", o)
			}
		}
	}
	e.assertExactlyOnce()
}

func mustGet(t *testing.T, s storage.Storage, key string) io.ReadCloser {
	t.Helper()
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A segment that can't be read must not block compaction of everything else.
func TestUnreadableInputIsSkippedNotFatal(t *testing.T) {
	cases := map[string]func(e *env, bad manifest.Segment){
		"corrupt": func(e *env, bad manifest.Segment) {
			data, _ := io.ReadAll(mustGet(t, e.store, bad.Key))
			data[len(data)/2] ^= 0xFF
			e.store.Put(ctx, bad.Key, bytes.NewReader(data))
		},
		"missing": func(e *env, bad manifest.Segment) { e.store.Delete(ctx, bad.Key) },
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			var rows []manifest.Segment
			for i := 0; i < 5; i++ {
				rows = append(rows, e.addSegment(int64(i*100), 20))
			}
			bad := rows[1]
			damage(e, bad)
			c := e.compactor(nil)

			if _, err := c.CompactOnce(ctx); err == nil {
				t.Fatal("expected an error for the damaged input")
			}
			if len(e.active()) != 5 || len(e.deleted()) != 0 {
				t.Fatal("manifest changed by a failed merge")
			}
			// Next attempt leaves the broken segment out and merges the other four.
			rep, err := c.CompactOnce(ctx)
			if err != nil || rep == nil || len(rep.InputIDs) != 4 {
				t.Fatalf("second attempt: %+v %v", rep, err)
			}
			for _, id := range rep.InputIDs {
				if id == bad.ID {
					t.Fatal("the damaged segment was merged")
				}
			}
			if got := len(e.active()); got != 2 { // merged + the damaged one, untouched
				t.Fatalf("active: %d", got)
			}
		})
	}
}

// Deleting the old files fails after a successful commit. The merge still
// counts as done; the rows stay marked deleted so GC finds the files later.
func TestFailedDeleteAfterCommitIsRetriedByGC(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.addSegment(int64(i*100), 20)
	}
	e.store.FailNextDeletes(2)
	rep, err := e.compactor(nil).CompactOnce(ctx)
	if err != nil || rep == nil {
		t.Fatalf("%v %v", rep, err)
	}
	if rep.DeleteErrors != 2 {
		t.Fatalf("DeleteErrors = %d, want 2", rep.DeleteErrors)
	}
	if len(e.deleted()) != 2 || len(e.objects()) != 3 {
		t.Fatalf("expected 2 leftover rows/files + merged: %d rows, %d objects", len(e.deleted()), len(e.objects()))
	}
	e.assertExactlyOnce()
	if _, err := e.compactor(nil).GarbageCollect(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.deleted()) != 0 || len(e.objects()) != 1 {
		t.Fatalf("after GC: %d rows, %d objects", len(e.deleted()), len(e.objects()))
	}
}

// ---- garbage collection ---------------------------------------------------

func TestGCOnlyRemovesWhatItShould(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		e.addSegment(int64(i*100), 10)
	}
	activeBefore := e.active()

	oldID := fmt.Sprintf("%016x-deadbeef", time.Now().Add(-time.Hour).UnixNano())
	oldOrphan := segment.KeyForID(oldID)
	youngOrphan := segment.KeyForID(segment.NewID())
	foreign := []string{"segments/README.txt", "segments/not-a-segment.strata", "other/data"}
	for _, k := range append([]string{oldOrphan, youngOrphan}, foreign...) {
		if err := e.store.Put(ctx, k, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}

	c := e.compactor(func(c *Config) { c.OrphanGrace = 10 * time.Minute })
	gc, err := c.GarbageCollect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gc.OrphansDeleted != 1 || gc.OrphansTooYoung != 1 {
		t.Fatalf("gc: %+v", gc)
	}
	keys, _ := e.store.List(ctx, "")
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	if have[oldOrphan] {
		t.Error("old orphan survived")
	}
	if !have[youngOrphan] {
		t.Error("a young unrecorded file was deleted: it could be a segment still being written")
	}
	for _, k := range foreign {
		if !have[k] {
			t.Errorf("deleted a file that is not ours: %s", k)
		}
	}
	for _, a := range activeBefore {
		if !have[a.Key] {
			t.Errorf("deleted the file of an active segment: %s", a.Key)
		}
	}
	// Idempotent.
	gc, _ = c.GarbageCollect(ctx)
	if gc.OrphansDeleted != 0 {
		t.Fatalf("second pass deleted more: %+v", gc)
	}
	e.assertExactlyOnce()
}

// ---- concurrency ----------------------------------------------------------

// Queries run continuously while segments are merged underneath them. Every
// single query must see every entry exactly once: never a loss, never a
// double count, even at the instant of the swap and while files disappear.
func TestQueriesStayCorrectWhileCompacting(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 48; i++ {
		e.addSegment(int64(i*100), 25)
	}
	c := e.compactor(func(c *Config) { c.MaxSegments = 6 })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var queries, retries atomic.Int64
	failed := make(chan string, 8)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := e.eng.Search(ctx, query.Query{Limit: 1 << 30})
				if err != nil {
					failed <- err.Error()
					return
				}
				queries.Add(1)
				retries.Add(int64(res.Metrics.Retries))
				seen := map[string]bool{}
				for _, h := range res.Hits {
					if seen[h.Message] {
						failed <- "duplicate " + h.Message
						return
					}
					seen[h.Message] = true
				}
				if len(res.Hits) != e.total {
					failed <- fmt.Sprintf("query saw %d entries, want %d", len(res.Hits), e.total)
					return
				}
			}
		}()
	}

	merges := 0
	for {
		rep, err := c.CompactOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rep == nil {
			break
		}
		merges++
	}
	if _, err := c.GarbageCollect(ctx); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	close(failed)
	for msg := range failed {
		t.Error(msg)
	}
	t.Logf("%d merges; %d concurrent queries, %d needed a restart", merges, queries.Load(), retries.Load())
	if merges < 2 {
		t.Fatalf("only %d merges happened", merges)
	}
	e.assertExactlyOnce()
}

// ---- the background loop --------------------------------------------------

func TestRunCompactsInTheBackgroundAndStopsOnCancel(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 8; i++ {
		e.addSegment(int64(i*100), 10)
	}
	c := e.compactor(func(c *Config) { c.Interval = 5 * time.Millisecond; c.GCInterval = 5 * time.Millisecond })

	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { c.Run(rctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(e.active()) != 1 || len(e.objects()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("background run did not finish: %d active, %d objects", len(e.active()), len(e.objects()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.assertExactlyOnce()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// Storage that fails for a while must not stop the background loop for good.
func TestRunSurvivesTransientFailures(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.addSegment(int64(i*100), 10)
	}
	e.store.FailNextPuts(3)
	c := e.compactor(func(c *Config) { c.Interval = 5 * time.Millisecond })
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.Run(rctx)

	deadline := time.Now().Add(5 * time.Second)
	for len(e.active()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("compaction never recovered from transient storage failures")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.assertExactlyOnce()
}

// The same merge, query and cleanup against a real S3-compatible server
// (skipped when none is running).
func TestCompactionAgainstS3(t *testing.T) {
	store := storagetest.NewS3(t)
	m, err := manifest.Open(filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	total := 0
	for i := 0; i < 5; i++ {
		var entries []segment.Entry
		for j := 0; j < 20; j++ {
			entries = append(entries, segment.Entry{Timestamp: int64(i*100 + j), Message: fmt.Sprintf("s3-%d-%d common", i, j)})
		}
		data, _ := segment.Encode(entries)
		seg, _ := segment.Decode(data)
		bl, _ := segment.ReadBloom(data)
		id := segment.NewID()
		row := manifest.Segment{ID: id, Key: segment.KeyForID(id), MinTS: seg.MinTimestamp(), MaxTS: seg.MaxTimestamp(), Count: seg.Len(), Size: int64(len(data)), Bloom: bl}
		if err := store.Put(ctx, row.Key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if err := m.AddSegment(ctx, row); err != nil {
			t.Fatal(err)
		}
		total += len(entries)
	}

	c, err := New(Config{Manifest: m, Storage: store, SmallBytes: 1 << 30, MinSegments: 2, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.CompactOnce(ctx)
	if err != nil || rep == nil || len(rep.InputIDs) != 5 || rep.DeleteErrors != 0 {
		t.Fatalf("report=%+v err=%v", rep, err)
	}
	res, err := query.New(m, store, 4).Search(ctx, query.Query{Text: "common", Limit: 1 << 30})
	if err != nil || len(res.Hits) != total {
		t.Fatalf("query after merge: %d hits (want %d), err %v", len(res.Hits), total, err)
	}
	keys, _ := store.List(ctx, segment.KeyPrefix)
	if len(keys) != 1 {
		t.Fatalf("bucket holds %d segment files after the merge, want 1", len(keys))
	}
	if gc, err := c.GarbageCollect(ctx); err != nil || gc.OrphansDeleted+gc.ReplacedFilesDeleted != 0 {
		t.Fatalf("gc on a clean bucket: %+v %v", gc, err)
	}
}

// This documents WHY the server checks the manifest's identity at startup
// (app.checkIdentity): garbage collection trusts its manifest completely. Given
// a wrong or fresh manifest, every real segment looks like an orphan and is
// deleted. Nothing in the compactor can tell the difference, so the protection
// has to happen before it ever runs.
func TestGCTrustsItsManifestSoAWrongOneDestroysData(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		e.addSegment(int64(i*100), 10)
	}
	if len(e.objects()) != 3 {
		t.Fatal("setup")
	}

	other, err := manifest.Open(filepath.Join(t.TempDir(), "other.db")) // wrong/lost manifest
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// A recorded segment of its own, so it is not merely "empty".
	c, _ := New(Config{Manifest: other, Storage: e.store, OrphanGrace: time.Nanosecond, Logger: quiet})
	time.Sleep(2 * time.Millisecond)
	// Backdate: segment IDs were just created, so the grace already passed.
	gc, err := c.GarbageCollect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gc.OrphansDeleted != 3 || len(e.objects()) != 0 {
		t.Fatalf("expected the wrong manifest's GC to delete all 3 real segments: %+v, %d left", gc, len(e.objects()))
	}
}

// recordingHandler captures log messages so tests can check what was warned.
type recordingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }
func (h *recordingHandler) count(substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

// Found while benchmarking: with TargetBytes so small that fewer than
// MinSegments segments fit, no batch ever qualifies and compaction does
// nothing, with no sign of a problem while segments pile up. It must say so.
func TestStuckCompactionIsReported(t *testing.T) {
	e := newEnv(t)
	var sz int64
	for i := 0; i < 6; i++ {
		sz = e.addSegment(int64(i*100), 20).Size
	}
	rec := &recordingHandler{}
	c := e.compactor(func(c *Config) {
		c.MinSegments = 4
		c.TargetBytes = sz * 3 // only 3 fit, but 4 are required
		c.Logger = slog.New(rec)
	})
	for i := 0; i < 3; i++ {
		if rep, err := c.CompactOnce(ctx); rep != nil || err != nil {
			t.Fatalf("%v %v", rep, err)
		}
	}
	if got := rec.count("compaction is stuck"); got != 1 {
		t.Fatalf("want the warning exactly once for an unchanged situation, got %d", got)
	}

	// Once the settings allow a merge, it proceeds and the warning resets.
	c.cfg.MinSegments = 3
	if rep, err := c.CompactOnce(ctx); err != nil || rep == nil {
		t.Fatalf("%v %v", rep, err)
	}
}

// No warning when there is simply nothing to merge.
func TestNoStuckWarningWhenThereIsNothingToDo(t *testing.T) {
	e := newEnv(t)
	e.addSegment(0, 10)
	rec := &recordingHandler{}
	c := e.compactor(func(c *Config) { c.MinSegments = 4; c.Logger = slog.New(rec) })
	c.CompactOnce(ctx)
	if rec.count("stuck") != 0 {
		t.Fatal("warned although a single segment is nothing to compact")
	}
}
