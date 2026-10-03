package bench

import (
	"math"
	"sort"
	"time"
)

// Percentile returns the p-th percentile (0 < p <= 100) of the durations using
// the nearest-rank method: the smallest value such that at least p percent of
// the samples are less than or equal to it. It does not interpolate, so every
// reported number is a latency that really happened. Returns 0 for no samples.
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Mean returns the average of the samples.
func Mean(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	return sum / time.Duration(len(samples))
}
