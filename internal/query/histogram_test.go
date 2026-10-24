package query

import (
	"math"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
)

// The point of the histogram: it counts every match, including the ones the
// limit cut off, without reading anything a search wouldn't read anyway.
func TestHistogramCountsEveryMatchNotOnlyTheReturnedOnes(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2005, 6, 20, 13, 0, 0, 0, time.UTC).UnixNano()
	sec := int64(time.Second)
	// Three segments, ten minutes apart, one line a second for five minutes.
	// "rare" is only in the middle one, so the others are skipped by bloom.
	for s := 0; s < 3; s++ {
		var entries []segment.Entry
		for i := 0; i < 300; i++ {
			msg := "common line"
			if s == 1 && i%10 == 0 {
				msg += " rare"
			}
			entries = append(entries, segment.Entry{Timestamp: base + int64(s)*600*sec + int64(i)*sec, Message: msg})
		}
		e.addSegment(entries)
	}

	res := e.search(Query{Text: "common", Limit: 5})
	h := res.Histogram
	if h == nil {
		t.Fatal("no histogram")
	}
	if len(res.Hits) != 5 || h.Total() != 900 {
		t.Fatalf("%d hits, histogram total %d; want 5 and 900", len(res.Hits), h.Total())
	}
	// 25 minutes of data: exactly MaxBuckets buckets of 15 s, the first
	// starting on the first line.
	if h.Width != 15*sec || h.Start != base || len(h.Counts) != 100 {
		t.Fatalf("width %v start %d buckets %d", time.Duration(h.Width), h.Start-base, len(h.Counts))
	}
	for i, c := range h.Counts {
		inData := (i/20)%2 == 0 // 5 minutes of lines, then 5 without
		if inData && c != 15 || !inData && c != 0 {
			t.Fatalf("bucket %d: %d", i, c)
		}
	}

	rare := e.search(Query{Text: "rare", Limit: 1})
	if rare.Metrics.SkippedByBloom != 2 || rare.Histogram.Total() != 30 {
		t.Fatalf("skipped by bloom %d, total %d; want 2 and 30", rare.Metrics.SkippedByBloom, rare.Histogram.Total())
	}
	// The axis still covers every segment in the time range, read or not, so
	// a chart shows where the matches are not as well as where they are.
	if rare.Histogram.Start != base || len(rare.Histogram.Counts) != 100 {
		t.Fatalf("rare histogram starts %d with %d buckets", rare.Histogram.Start-base, len(rare.Histogram.Counts))
	}

	// A time range clips the buckets to the range.
	win := e.search(Query{Text: "common", From: base + 600*sec, To: base + 660*sec})
	if win.Histogram.Total() != 60 || win.Histogram.Width != sec || len(win.Histogram.Counts) != 60 {
		t.Fatalf("window: total %d, width %v, %d buckets", win.Histogram.Total(), time.Duration(win.Histogram.Width), len(win.Histogram.Counts))
	}

	// Nothing in range, nothing to count.
	if none := e.search(Query{Text: "common", From: base + 3600*sec}); none.Histogram != nil {
		t.Fatalf("histogram for an empty range: %+v", none.Histogram)
	}
}

func TestHistogramBuckets(t *testing.T) {
	at := func(s string) int64 {
		ts, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return ts.UnixNano()
	}
	seg := func(lo, hi int64) manifest.PlanRow {
		return manifest.PlanRow{Segment: manifest.Segment{MinTS: lo, MaxTS: hi}, InRange: true}
	}
	cases := []struct {
		name     string
		plan     []manifest.PlanRow
		from, to int64
		width    time.Duration
		buckets  int
	}{
		{"one hour", []manifest.PlanRow{seg(at("2005-06-20 13:00"), at("2005-06-20 13:59"))}, math.MinInt64, math.MaxInt64, time.Minute, 60},
		{"one day", []manifest.PlanRow{seg(at("2005-06-20 00:00"), at("2005-06-20 23:59"))}, math.MinInt64, math.MaxInt64, 15 * time.Minute, 96},
		// BGL's whole span, about 215 days; aligning the first bucket to a
		// multiple of 3 days adds one.
		{"all of BGL", []manifest.PlanRow{seg(at("2005-06-03 15:42"), at("2005-10-01 00:00")), seg(at("2005-10-01 00:00"), at("2006-01-04 08:00"))}, math.MinInt64, math.MaxInt64, 3 * 24 * time.Hour, 73},
		// The query's range trims the data's extent.
		{"clipped", []manifest.PlanRow{seg(at("2005-06-01 00:00"), at("2005-07-01 00:00"))}, at("2005-06-20 13:00"), at("2005-06-20 14:00"), time.Minute, 60},
		// Decades of data still fit in MaxBuckets.
		{"centuries", []manifest.PlanRow{seg(at("1700-01-01 00:00"), at("2200-01-01 00:00"))}, math.MinInt64, math.MaxInt64, 8 * 365 * 24 * time.Hour, 63},
	}
	for _, c := range cases {
		h := newHistogram(c.plan, c.from, c.to)
		if h == nil || time.Duration(h.Width) != c.width || len(h.Counts) != c.buckets {
			t.Errorf("%s: got %+v, want width %v and %d buckets", c.name, h, c.width, c.buckets)
			continue
		}
		if len(h.Counts) > MaxBuckets || h.Start%h.Width != 0 && h.Start > 0 {
			t.Errorf("%s: %d buckets, start %d not aligned", c.name, len(h.Counts), h.Start)
		}
	}

	// Only segments in the time range count towards the extent.
	out := manifest.PlanRow{Segment: manifest.Segment{MinTS: 0, MaxTS: 1}}
	if h := newHistogram([]manifest.PlanRow{out}, math.MinInt64, math.MaxInt64); h != nil {
		t.Errorf("histogram for a plan with nothing in range: %+v", h)
	}
	// Before 1970 a timestamp is negative; it must still land in its own bucket.
	h := &Histogram{Start: -10, Width: 5, Counts: make([]int64, 4)}
	for ts, want := range map[int64]int{-10: 0, -6: 0, -5: 1, -1: 1, 0: 2, 9: 3, 99: 3, -99: 0} {
		if got := h.bucket(ts); got != want {
			t.Errorf("bucket(%d) = %d, want %d", ts, got, want)
		}
	}
}
