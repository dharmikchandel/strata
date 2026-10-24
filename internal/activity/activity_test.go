package activity

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLogKeepsTheLatestAndHandsEachOutOnce(t *testing.T) {
	l := NewLog(3)
	for i := 0; i < 5; i++ {
		l.Add(Event{Kind: Sealed, Segment: fmt.Sprint("s", i)})
	}
	all := l.Since(0)
	if len(all) != 3 || all[0].Seq != 3 || all[2].Seq != 5 || all[2].Segment != "s4" || all[0].At.IsZero() {
		t.Fatalf("ring: %+v", all)
	}
	// A poller that saw up to 4 gets only 5.
	if got := l.Since(4); len(got) != 1 || got[0].Seq != 5 {
		t.Fatalf("since 4: %+v", got)
	}
	if got := l.Since(5); len(got) != 0 {
		t.Fatalf("since 5: %+v", got)
	}
}

func TestLogIsSafeForConcurrentUse(t *testing.T) {
	l := NewLog(100)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				l.Add(Event{Kind: Merged})
				l.Since(0)
			}
		}()
	}
	wg.Wait()
	got := l.Since(0)
	if len(got) != 100 || got[99].Seq != 400 {
		t.Fatalf("got %d events, last seq %d", len(got), got[len(got)-1].Seq)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("sequence broken at %d: %d then %d", i, got[i-1].Seq, got[i].Seq)
		}
	}
}

func TestRateAveragesOverTheWindow(t *testing.T) {
	var n int64
	r := NewRate(func() int64 { return n }, 10*time.Second)
	t0 := time.Unix(1000, 0)
	if r.Sample(t0); r.PerSecond() != 0 {
		t.Fatal("a rate from one sample")
	}
	// 100 lines/s for 20 s, then nothing for 5 s.
	for s := 1; s <= 25; s++ {
		if s <= 20 {
			n += 100
		}
		r.Sample(t0.Add(time.Duration(s) * time.Second))
	}
	// The last 10 s hold 5 s at 100/s and 5 s at 0.
	if got := r.PerSecond(); got != 50 {
		t.Fatalf("rate %v, want 50", got)
	}
}
