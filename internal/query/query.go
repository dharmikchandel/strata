// Package query answers search requests, reading as little as possible.
//
// A search goes through three filters, cheapest first. Each one only decides
// "can I skip this segment?", and each is allowed to be wrong in one direction
// only: it may keep a segment that turns out to have no match (wasted work),
// but never skip one that has a match (lost data).
//
//  1. Time range, answered by the manifest (one SQLite query, no storage reads).
//  2. Bloom filter, also kept in the manifest (in memory, no storage reads).
//  3. The segment's own inverted index, after fetching the segment.
//
// Only segments that survive 1 and 2 are fetched from object storage. The
// per-query Metrics report how many were skipped at each stage, which is the
// evidence that the design avoids reading data it doesn't need.
package query

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/dharmikchandel/strata/internal/bloom"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
)

const (
	// DefaultLimit applies when Query.Limit is zero. Results are held in
	// memory, so an unbounded query could use unbounded memory.
	DefaultLimit = 1000
	// DefaultConcurrency is how many segments are fetched in parallel.
	DefaultConcurrency = 8
	// maxAttempts bounds how often a query restarts after finding that a
	// segment it planned to read has been replaced (see Search).
	maxAttempts = 3
)

// Query describes a search.
type Query struct {
	// Text is full-text search on the message. It is split into terms the same
	// way messages were when indexed (lowercase letters and digits); a line
	// matches only if it contains ALL terms, in any order.
	Text string
	// Tags are optional exact-match filters; a line must have every one.
	Tags map[string]string
	// From (inclusive) and To (exclusive) bound entry timestamps in unix
	// nanoseconds. Zero means unbounded on that side.
	From, To int64
	// Limit caps the number of results; zero means DefaultLimit. Results are
	// the earliest matches, in timestamp order.
	Limit int
}

// Hit is one matching log line.
type Hit struct {
	SegmentID string
	segment.Entry
}

// Metrics describe how much work one query did. They are the proof that
// segments were skipped rather than read.
type Metrics struct {
	// SegmentsConsidered is every active segment in the manifest: the number
	// a naive scan would have had to read.
	SegmentsConsidered int
	// SkippedByTime were ruled out because their time range misses the query's.
	SkippedByTime int
	// SkippedByBloom were ruled out because a query term is definitely absent.
	SkippedByBloom int
	// SegmentsScanned were fetched from storage and searched.
	SegmentsScanned int
	// BytesRead is the total size of the segments fetched.
	BytesRead int64
	// Retries counts restarts caused by segments disappearing mid-query.
	Retries int
	// Latency is the wall-clock time of the whole Search call.
	Latency time.Duration
}

func (m Metrics) String() string {
	return fmt.Sprintf("considered=%d skipped_time=%d skipped_bloom=%d scanned=%d bytes_read=%d retries=%d latency=%s",
		m.SegmentsConsidered, m.SkippedByTime, m.SkippedByBloom, m.SegmentsScanned, m.BytesRead, m.Retries, m.Latency)
}

// Outcome says what a search did with one segment.
type Outcome int

const (
	SkippedByTime Outcome = iota + 1
	SkippedByBloom
	Scanned
)

// SegmentOutcome is the result of a search for one segment.
type SegmentOutcome struct {
	ID           string
	MinTS, MaxTS int64
	Size         int64
	Outcome      Outcome
	BytesRead    int64 // Scanned only
	Hits         int   // lines from this segment in the final result
}

// Result is the answer to a Search.
type Result struct {
	// Hits are sorted by timestamp, ties broken by manifest order (segment
	// min timestamp, then ID) and then line order, so results are deterministic.
	Hits []Hit
	// Truncated is true if more matches existed than Limit allowed.
	Truncated bool
	Metrics   Metrics
	// Segments lists what happened to every active segment, oldest first.
	Segments []SegmentOutcome
}

// Engine runs searches over a manifest and the segments it describes.
type Engine struct {
	manifest    *manifest.Manifest
	store       storage.Storage
	concurrency int
}

// New returns an Engine. concurrency <= 0 uses DefaultConcurrency.
func New(m *manifest.Manifest, store storage.Storage, concurrency int) *Engine {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return &Engine{manifest: m, store: store, concurrency: concurrency}
}

// ErrSegmentMissing means a segment listed in the manifest was gone from storage
// (after the retries described on Search).
var ErrSegmentMissing = errors.New("query: segment missing from storage")

// Search runs q.
//
// Races with compaction: the manifest and storage are two systems, so a query
// can read the manifest, and then find that a segment it listed has been
// replaced and its file deleted. That is not data loss: the replacement
// segment holds the same entries and the manifest now lists it. So when a
// segment is missing, Search starts over from a fresh manifest snapshot (up to
// maxAttempts times). A segment that stays missing is reported as an error.
func (e *Engine) Search(ctx context.Context, q Query) (*Result, error) {
	start := time.Now()
	retries := 0
	for attempt := 1; ; attempt++ {
		res, err := e.search(ctx, q)
		if errors.Is(err, ErrSegmentMissing) && attempt < maxAttempts {
			retries++
			continue
		}
		if err != nil {
			return nil, err
		}
		res.Metrics.Retries = retries
		res.Metrics.Latency = time.Since(start)
		return res, nil
	}
}

func (e *Engine) search(ctx context.Context, q Query) (*Result, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	from, to := q.From, q.To
	if from == 0 {
		from = math.MinInt64
	}
	if to == 0 {
		to = math.MaxInt64
	}
	terms := queryTerms(q)

	var m Metrics
	// One statement gives every active segment and whether it overlaps the time
	// range (filter 1), plus the bloom filters of the ones that do. A single
	// snapshot, so the counts below always add up even while compaction runs.
	plan, err := e.manifest.Plan(ctx, from, to)
	if err != nil {
		return nil, err
	}
	m.SegmentsConsidered = len(plan)

	// Filter 2: bloom filters, from the manifest. No storage reads yet.
	outcomes := make([]SegmentOutcome, len(plan))
	var candidates []manifest.Segment
	var candidateAt []int // index into outcomes of each candidate
	for i, r := range plan {
		o := SegmentOutcome{ID: r.ID, MinTS: r.MinTS, MaxTS: r.MaxTS, Size: r.Size}
		switch {
		case !r.InRange:
			o.Outcome = SkippedByTime
			m.SkippedByTime++
		case !mayContainAll(r.Segment, terms):
			o.Outcome = SkippedByBloom
			m.SkippedByBloom++
		default:
			o.Outcome = Scanned
			candidates = append(candidates, r.Segment)
			candidateAt = append(candidateAt, i)
		}
		outcomes[i] = o
	}

	// Filter 3: fetch the survivors in parallel and search each one.
	perSegment := make([][]Hit, len(candidates))
	bytesRead := make([]int64, len(candidates))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	for i, s := range candidates {
		g.Go(func() error {
			// limit+1: one extra hit per segment is what lets the merge below
			// notice that more lines matched than were asked for. Cutting a
			// segment at exactly limit would hide that (Truncated would stay
			// false when one segment alone had more matches than the limit).
			hits, n, err := e.scan(gctx, s, terms, from, to, limit+1)
			perSegment[i], bytesRead[i] = hits, n
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	m.SegmentsScanned = len(candidates)
	for k, n := range bytesRead {
		m.BytesRead += n
		outcomes[candidateAt[k]].BytesRead = n
	}

	// Merge. Each segment's hits are already in timestamp order, and a stable
	// sort of the concatenation keeps ties in segment order, so the output is
	// deterministic.
	var hits []Hit
	for _, h := range perSegment {
		hits = append(hits, h...)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Timestamp < hits[j].Timestamp })
	truncated := false
	if len(hits) > limit {
		hits, truncated = hits[:limit], true
	}
	// Which segments the returned lines came from.
	fromSegment := make(map[string]int, len(candidates))
	for _, h := range hits {
		fromSegment[h.SegmentID]++
	}
	for i := range outcomes {
		outcomes[i].Hits = fromSegment[outcomes[i].ID]
	}
	return &Result{Hits: hits, Truncated: truncated, Metrics: m, Segments: outcomes}, nil
}

// scan fetches one segment and returns its matches (at most limit, earliest
// first; the caller passes the query limit plus one, see search) and the number of bytes fetched.
func (e *Engine) scan(ctx context.Context, s manifest.Segment, terms []string, from, to int64, limit int) ([]Hit, int64, error) {
	r, err := e.store.Get(ctx, s.Key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, 0, fmt.Errorf("%w: %s (%s)", ErrSegmentMissing, s.ID, s.Key)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("query: fetch segment %s: %w", s.ID, err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil, 0, fmt.Errorf("query: read segment %s: %w", s.ID, err)
	}
	n := int64(len(data))

	seg, err := segment.Decode(data)
	if err != nil {
		// A damaged segment is an error, not an empty result: skipping it
		// silently would return incomplete answers that look complete.
		return nil, n, fmt.Errorf("query: segment %s (%s): %w", s.ID, s.Key, err)
	}
	lo, hi, err := seg.IDRange(from, to)
	if err != nil {
		return nil, n, fmt.Errorf("query: segment %s: %w", s.ID, err)
	}
	ids := matchingIDs(seg, terms, lo, hi)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	hits := make([]Hit, 0, len(ids))
	for _, id := range ids {
		entry, err := seg.Entry(id)
		if err != nil {
			return nil, n, fmt.Errorf("query: segment %s: %w", s.ID, err)
		}
		hits = append(hits, Hit{SegmentID: s.ID, Entry: entry})
	}
	return hits, n, nil
}

// matchingIDs returns the ascending line ids in [lo, hi) that contain every
// term. With no terms every line in the range matches.
func matchingIDs(seg *segment.Segment, terms []string, lo, hi uint32) []uint32 {
	if len(terms) == 0 {
		ids := make([]uint32, 0, hi-lo)
		for id := lo; id < hi; id++ {
			ids = append(ids, id)
		}
		return ids
	}
	lists := make([][]uint32, len(terms))
	for i, t := range terms {
		lists[i] = seg.Lookup(t)
		if len(lists[i]) == 0 {
			return nil // a term with no postings means no line can match
		}
	}
	// Start from the shortest list: the result can't be longer than it, and
	// each id only needs a binary search in the others.
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	var out []uint32
	for _, id := range lists[0] {
		if id < lo || id >= hi {
			continue
		}
		ok := true
		for _, other := range lists[1:] {
			if i := sort.Search(len(other), func(k int) bool { return other[k] >= id }); i == len(other) || other[i] != id {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, id)
		}
	}
	return out
}

// queryTerms turns a Query into the index terms every matching line must have:
// the distinct tokens of Text plus one term per tag.
func queryTerms(q Query) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range segment.Tokenize(q.Text) {
		if !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	var tagTerms []string
	for k, v := range q.Tags {
		tagTerms = append(tagTerms, segment.TagTerm(k, v))
	}
	sort.Strings(tagTerms) // map order is random; keep behaviour deterministic
	return append(terms, tagTerms...)
}

// mayContainAll applies the bloom filter. If the segment has no usable filter
// the answer is "maybe": the safe direction, costing a read but never data.
func mayContainAll(s manifest.Segment, terms []string) bool {
	if len(terms) == 0 || len(s.Bloom) == 0 {
		return true
	}
	f, err := bloom.Unmarshal(s.Bloom)
	if err != nil {
		return true
	}
	for _, t := range terms {
		if !f.MayContain(t) {
			return false
		}
	}
	return true
}
