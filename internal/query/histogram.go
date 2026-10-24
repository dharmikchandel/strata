package query

import (
	"math"
	"time"

	"github.com/dharmikchandel/strata/internal/manifest"
)

// MaxBuckets bounds the histogram's length, so a response stays small and a
// chart has bars wide enough to point at whatever the time range.
const MaxBuckets = 100

// Histogram counts matching lines per time bucket.
//
// It costs no extra reads. A segment that is searched is downloaded whole
// anyway, and its index already gives every matching line, not only the first
// few that are returned; counting them only needs each line's timestamp. And
// a segment that was skipped holds no match by construction, so the counts are
// the exact number of matching lines in the whole query, by time.
type Histogram struct {
	Start  int64 // start of the first bucket, unix nanoseconds
	Width  int64 // bucket width, nanoseconds
	Counts []int64
}

// bucketWidths are the widths a histogram may use: round numbers on a clock or
// calendar, so bucket edges fall on times a person would name.
var bucketWidths = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	24 * time.Hour, 2 * 24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour, 14 * 24 * time.Hour,
	30 * 24 * time.Hour, 90 * 24 * time.Hour, 365 * 24 * time.Hour,
}

// newHistogram returns empty buckets covering the part of [from, to) that the
// segments in range hold data for, or nil when no segment is in range. Using
// the data's extent rather than the query's keeps an unbounded query (from or
// to zero) from spanning centuries.
func newHistogram(plan []manifest.PlanRow, from, to int64) *Histogram {
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for _, r := range plan {
		if r.InRange {
			lo, hi = min(lo, r.MinTS), max(hi, r.MaxTS)
		}
	}
	if lo > hi {
		return nil
	}
	lo = max(lo, from)
	if hi < math.MaxInt64 {
		hi++ // MaxTS is inclusive; the span is half-open
	}
	hi = min(hi, to)
	if hi <= lo {
		return nil
	}

	var width int64
	for _, w := range bucketWidths {
		width = int64(w)
		if spanBuckets(lo, hi, width) <= MaxBuckets {
			break
		}
	}
	// Beyond the largest round width (decades of data), use whole multiples
	// of it.
	for spanBuckets(lo, hi, width) > MaxBuckets {
		width *= 2
	}
	start := floorDiv(lo, width) * width
	return &Histogram{Start: start, Width: width, Counts: make([]int64, spanBuckets(lo, hi, width))}
}

// spanBuckets is how many aligned buckets of width w cover [lo, hi).
func spanBuckets(lo, hi, w int64) int64 {
	return floorDiv(hi-1, w) - floorDiv(lo, w) + 1
}

// floorDiv rounds towards minus infinity (Go's / rounds towards zero), so
// timestamps before 1970 still land in the bucket that contains them.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// bucket returns the index of the bucket holding ts, clamped to the ends.
func (h *Histogram) bucket(ts int64) int {
	i := floorDiv(ts-h.Start, h.Width)
	return int(min(max(i, 0), int64(len(h.Counts)-1)))
}

// Total is the number of matching lines the histogram counted; zero for a nil
// histogram (nothing was in range).
func (h *Histogram) Total() int64 {
	if h == nil {
		return 0
	}
	var n int64
	for _, c := range h.Counts {
		n += c
	}
	return n
}
