package gen

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
)

// Searcher is the part of the query client verification needs.
type Searcher interface {
	Search(ctx context.Context, in *stratav1.SearchRequest, opts ...grpc.CallOption) (*stratav1.SearchResponse, error)
}

// VerifyOptions controls verification.
type VerifyOptions struct {
	Workers int
	// Settle is the pause between attempts. A line the server acknowledged can
	// still be in its buffer (not yet sealed, so not yet searchable); a missing
	// line is therefore looked for again before it is called lost.
	Settle      time.Duration
	MaxAttempts int // default 3
	Logger      *slog.Logger
}

// SeqRange is a run of consecutive sequence numbers from one source.
type SeqRange struct {
	Source   int
	From, To uint64 // inclusive
}

// VerifyResult says what was found.
type VerifyResult struct {
	Windows      int // time windows checked
	Unverifiable int // windows with more lines than one search can return
	Acked        int64
	Found        int64 // distinct acknowledged lines found
	Lost         int64 // acknowledged but not found (in windows that could be fully checked)
	Duplicates   int64 // lines stored more than once (a resent batch whose first copy had been stored)
	LostRanges   []SeqRange
	Attempts     int
}

func (v *VerifyResult) String() string {
	s := fmt.Sprintf("verification: %d windows checked, %d lines acknowledged, %d found, LOST %d, duplicates %d",
		v.Windows, v.Acked, v.Found, v.Lost, v.Duplicates)
	if v.Unverifiable > 0 {
		s += fmt.Sprintf(", %d windows too dense to check", v.Unverifiable)
	}
	if len(v.LostRanges) > 0 {
		var parts []string
		for i, r := range v.LostRanges {
			if i == 5 {
				parts = append(parts, "...")
				break
			}
			parts = append(parts, fmt.Sprintf("source %d #%d-%d", r.Source, r.From, r.To))
		}
		s += "\n  lost sequence numbers: " + strings.Join(parts, ", ")
	}
	return s
}

type windowState struct {
	bucket       int64
	expected     int
	unique       int
	dups         int
	unverifiable bool
}

// Verify looks every acknowledged line up again, window by window, and reports
// which are missing. A window is a short stretch of timestamps, sized so that one
// search can return all of it; lines are matched by the marker the generator put
// on each (run, source and sequence number), so a loss is identified exactly.
func Verify(ctx context.Context, q Searcher, rep *Report, opt VerifyOptions) (*VerifyResult, error) {
	if opt.Workers < 1 {
		opt.Workers = 1
	}
	if opt.MaxAttempts < 1 {
		opt.MaxAttempts = 3
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}

	buckets := make([]int64, 0, len(rep.Expected))
	for b := range rep.Expected {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
	windows := make([]*windowState, len(buckets))
	for i, b := range buckets {
		windows[i] = &windowState{bucket: b, expected: rep.Expected[b]}
	}

	found := make([][]uint64, len(rep.AckedBySource)) // a bit per sequence number, per source
	for i, n := range rep.AckedBySource {
		found[i] = make([]uint64, (n+63)/64)
	}
	var mu sync.Mutex
	mark := func(src int, seq uint64) {
		if src < 0 || src >= len(found) || seq >= rep.AckedBySource[src] {
			return // not one of the lines the server acknowledged to us
		}
		found[src][seq/64] |= 1 << (seq % 64)
	}

	check := func(w *windowState) error {
		from := w.bucket * int64(rep.BucketWidth)
		resp, err := q.Search(ctx, &stratav1.SearchRequest{
			Text: rep.RunID, FromUnixNano: from, ToUnixNano: from + int64(rep.BucketWidth), Limit: 10000})
		if err != nil {
			return fmt.Errorf("searching window %d: %w", w.bucket, err)
		}
		seen := map[[2]uint64]bool{}
		hits := 0
		mu.Lock()
		defer mu.Unlock()
		for _, h := range resp.Hits {
			run, src, seq, ok := ParseMarker(h.Message)
			if !ok || run != rep.RunID {
				continue
			}
			hits++
			seen[[2]uint64{uint64(src), seq}] = true
			mark(src, seq)
		}
		w.unique, w.dups = len(seen), hits-len(seen)
		w.unverifiable = resp.Truncated || len(resp.Hits) >= 10000
		return nil
	}

	res := &VerifyResult{Windows: len(windows)}
	pending := windows
	for attempt := 1; attempt <= opt.MaxAttempts && len(pending) > 0; attempt++ {
		res.Attempts = attempt
		if err := runWindows(pending, opt.Workers, check); err != nil {
			return nil, err
		}
		var still []*windowState
		for _, w := range pending {
			if !w.unverifiable && w.unique < w.expected {
				still = append(still, w)
			}
		}
		pending = still
		if len(pending) > 0 && attempt < opt.MaxAttempts {
			opt.Logger.Info("some acknowledged lines were not found yet; waiting, then checking those windows again",
				"windows", len(pending), "attempt", attempt)
			select {
			case <-time.After(opt.Settle):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	for _, w := range windows {
		res.Acked += int64(w.expected)
		res.Duplicates += int64(w.dups)
		if w.unverifiable {
			res.Unverifiable++
			continue
		}
		res.Found += int64(w.unique)
		if w.unique < w.expected {
			res.Lost += int64(w.expected - w.unique)
		}
	}
	if res.Unverifiable == 0 {
		res.LostRanges = missingRanges(found, rep.AckedBySource)
	}
	return res, nil
}

// runWindows runs fn over the windows with a fixed number of workers and stops
// at the first error.
func runWindows(ws []*windowState, workers int, fn func(*windowState) error) error {
	jobs := make(chan *windowState)
	failed := make(chan struct{})
	var once sync.Once
	var firstErr error
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range jobs {
				if err := fn(w); err != nil {
					once.Do(func() { firstErr = err; close(failed) })
					return
				}
			}
		}()
	}
feed:
	for _, w := range ws {
		select {
		case jobs <- w:
		case <-failed:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

// missingRanges lists the runs of sequence numbers that were acknowledged but never found.
func missingRanges(found [][]uint64, acked []uint64) []SeqRange {
	var out []SeqRange
	for src, words := range found {
		var start uint64
		in := false
		for seq := uint64(0); seq < acked[src]; seq++ {
			missing := words[seq/64]&(1<<(seq%64)) == 0
			switch {
			case missing && !in:
				start, in = seq, true
			case !missing && in:
				out = append(out, SeqRange{Source: src, From: start, To: seq - 1})
				in = false
			}
		}
		if in {
			out = append(out, SeqRange{Source: src, From: start, To: acked[src] - 1})
		}
	}
	return out
}
