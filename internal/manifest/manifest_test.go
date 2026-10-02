package manifest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

var ctx = context.Background()

func openTemp(t *testing.T) (*Manifest, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.db")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, path
}

func seg(id string, minTS, maxTS int64, count int) Segment {
	return Segment{ID: id, Key: "segments/" + id + ".strata", MinTS: minTS, MaxTS: maxTS, Count: count, Size: int64(count) * 100}
}

func ids(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.ID
	}
	return out
}

func mustList(t *testing.T, m *Manifest, st Status) []Segment {
	t.Helper()
	segs, err := m.List(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	return segs
}

func TestAddAndList(t *testing.T) {
	m, _ := openTemp(t)
	for _, s := range []Segment{seg("b", 200, 300, 5), seg("a", 100, 250, 3), seg("c", 200, 200, 1)} {
		if err := m.AddSegment(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	got := mustList(t, m, StatusActive)
	// Ordered by min timestamp, ties by id.
	if want := []string{"a", "b", "c"}; fmt.Sprint(ids(got)) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", ids(got), want)
	}
	a := got[0]
	if a.Key != "segments/a.strata" || a.MinTS != 100 || a.MaxTS != 250 || a.Count != 3 || a.Size != 300 || a.Status != StatusActive || a.CreatedAt.IsZero() {
		t.Fatalf("bad row: %+v", a)
	}
	if d := mustList(t, m, StatusDeleted); len(d) != 0 {
		t.Fatalf("unexpected deleted segments: %v", d)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	m, path := openTemp(t)
	if err := m.AddSegment(ctx, seg("a", 1, 2, 1)); err != nil {
		t.Fatal(err)
	}
	m.Close()
	m2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if got := mustList(t, m2, StatusActive); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %v", got)
	}
}

func TestInvalidSegmentsAreRejected(t *testing.T) {
	m, _ := openTemp(t)
	bad := map[string]Segment{
		"empty id":      {Key: "k", MinTS: 1, MaxTS: 2, Count: 1},
		"empty key":     {ID: "x", MinTS: 1, MaxTS: 2, Count: 1},
		"min after max": {ID: "x", Key: "k", MinTS: 3, MaxTS: 2, Count: 1},
		"zero count":    {ID: "x", Key: "k", MinTS: 1, MaxTS: 2},
		"negative size": {ID: "x", Key: "k", MinTS: 1, MaxTS: 2, Count: 1, Size: -1},
	}
	for name, s := range bad {
		if err := m.AddSegment(ctx, s); !errors.Is(err, ErrInvalidSegment) {
			t.Errorf("%s: want ErrInvalidSegment, got %v", name, err)
		}
	}
	if got := mustList(t, m, StatusActive); len(got) != 0 {
		t.Fatalf("invalid segments were stored: %v", got)
	}
}

func TestDatabaseRejectsBadRowsEvenIfCodeDoesNot(t *testing.T) {
	// The CHECK constraints are a second line of defence behind validate().
	m, _ := openTemp(t)
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO segments VALUES ('x','k',5,1,1,1,'active',0)`) // min_ts > max_ts
	if err == nil {
		t.Fatal("database accepted min_ts > max_ts")
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO segments VALUES ('y','k2',1,2,1,1,'bogus',0)`)
	if err == nil {
		t.Fatal("database accepted an unknown status")
	}
}

func TestDuplicateIDAndKey(t *testing.T) {
	m, _ := openTemp(t)
	if err := m.AddSegment(ctx, seg("a", 1, 2, 1)); err != nil {
		t.Fatal(err)
	}
	if err := m.AddSegment(ctx, seg("a", 5, 6, 1)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same id: want ErrDuplicate, got %v", err)
	}
	other := seg("b", 5, 6, 1)
	other.Key = "segments/a.strata"
	if err := m.AddSegment(ctx, other); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same key: want ErrDuplicate, got %v", err)
	}
	if got := mustList(t, m, StatusActive); len(got) != 1 {
		t.Fatalf("got %v", ids(got))
	}
}

func TestReplaceSwapsSegments(t *testing.T) {
	m, _ := openTemp(t)
	m.AddSegment(ctx, seg("a", 1, 10, 4))
	m.AddSegment(ctx, seg("b", 11, 20, 6))
	if err := m.Replace(ctx, []Segment{seg("ab", 1, 20, 10)}, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := ids(mustList(t, m, StatusActive)); fmt.Sprint(got) != "[ab]" {
		t.Fatalf("active: %v", got)
	}
	if got := ids(mustList(t, m, StatusDeleted)); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("deleted: %v", got)
	}
}

// A failure in the second step must undo the first: this is atomicity.
func TestReplaceRollsBackEverythingOnFailure(t *testing.T) {
	m, _ := openTemp(t)
	m.AddSegment(ctx, seg("a", 1, 10, 4))
	m.AddSegment(ctx, seg("old-deleted", 1, 5, 1))
	m.Replace(ctx, nil, []string{"old-deleted"})

	cases := map[string]struct {
		add    []Segment
		delete []string
		want   error
	}{
		"delete missing id":      {[]Segment{seg("new", 1, 10, 4)}, []string{"a", "ghost"}, ErrNotActive},
		"delete already deleted": {[]Segment{seg("new", 1, 10, 4)}, []string{"a", "old-deleted"}, ErrNotActive},
		"second add duplicates":  {[]Segment{seg("new", 1, 10, 4), seg("a", 1, 10, 4)}, []string{"a"}, ErrDuplicate},
	}
	for name, c := range cases {
		if err := m.Replace(ctx, c.add, c.delete); !errors.Is(err, c.want) {
			t.Fatalf("%s: want %v, got %v", name, c.want, err)
		}
		if got := ids(mustList(t, m, StatusActive)); fmt.Sprint(got) != "[a]" {
			t.Fatalf("%s: active set changed to %v (partial update!)", name, got)
		}
		if got := ids(mustList(t, m, StatusDeleted)); fmt.Sprint(got) != "[old-deleted]" {
			t.Fatalf("%s: deleted set changed to %v", name, got)
		}
	}
}

func TestMigrationsAreIdempotentAndVersionChecked(t *testing.T) {
	m, path := openTemp(t)
	m.AddSegment(ctx, seg("a", 1, 2, 1))
	m.Close()
	// Reopening must not re-run migrations (that would fail on "table exists").
	m2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var v int
	if err := m2.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, len(migrations))
	}
	// A database from a newer Strata must be refused, not silently misread.
	if _, err := m2.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	m2.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("opened a database with a newer schema version")
	}
}

func TestConcurrentWriters(t *testing.T) {
	m, _ := openTemp(t)
	const writers, each = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				if err := m.AddSegment(ctx, seg(id, int64(i), int64(i)+10, 1)); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent add failed: %v", err)
	}
	if got := mustList(t, m, StatusActive); len(got) != writers*each {
		t.Fatalf("got %d segments, want %d", len(got), writers*each)
	}
}

// Readers must never see a half-done swap. A "compactor" repeatedly replaces
// two active segments with one merged segment while readers check that the
// total entry count across active segments never changes. If a reader ever
// saw the new segment alongside the old ones (double counting) or neither
// (missing data), the total would be wrong.
func TestReadersNeverSeeAHalfFinishedSwap(t *testing.T) {
	m, _ := openTemp(t)
	const initial, perSegment = 64, 10
	for i := 0; i < initial; i++ {
		if err := m.AddSegment(ctx, seg(fmt.Sprintf("s%03d", i), int64(i), int64(i), perSegment)); err != nil {
			t.Fatal(err)
		}
	}
	const total = initial * perSegment

	stop := make(chan struct{})
	var readers sync.WaitGroup
	bad := make(chan string, 16)
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				segs, err := m.List(ctx, StatusActive)
				if err != nil {
					bad <- err.Error()
					return
				}
				sum := 0
				for _, s := range segs {
					sum += s.Count
				}
				if sum != total {
					bad <- fmt.Sprintf("reader saw %d entries across %d active segments, want %d", sum, len(segs), total)
					return
				}
			}
		}()
	}

	// Merge pairs until one segment is left.
	n := 0
	for {
		active := mustList(t, m, StatusActive)
		if len(active) < 2 {
			break
		}
		a, b := active[0], active[1]
		n++
		merged := seg(fmt.Sprintf("m%03d", n), a.MinTS, max(a.MaxTS, b.MaxTS), a.Count+b.Count)
		if err := m.Replace(ctx, []Segment{merged}, []string{a.ID, b.ID}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(bad)
	for msg := range bad {
		t.Error(msg)
	}
	if got := mustList(t, m, StatusActive); len(got) != 1 || got[0].Count != total {
		t.Fatalf("final state: %+v", got)
	}
	t.Logf("%d atomic swaps observed by 4 concurrent readers", n)
}
