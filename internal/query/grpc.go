package query

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/activity"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
)

const (
	// MaxLimit is the most hits one request may ask for. It bounds the size of
	// a response (and the memory used to build it).
	MaxLimit = 10_000
	// MaxTextBytes bounds the search text.
	MaxTextBytes = 4096
	// DefaultMaxConcurrent is how many searches may run at once.
	DefaultMaxConcurrent = 8
	// DefaultMaxOutcomes is the most per-segment results one response lists.
	DefaultMaxOutcomes = 1000
	// DefaultTimeout applies when the client sets no deadline of its own.
	DefaultTimeout = 30 * time.Second
	// MaxSystemSegments bounds the segment list in a System response.
	MaxSystemSegments = 2000
)

// ServiceConfig controls the gRPC query service. Zero values use defaults.
type ServiceConfig struct {
	// MaxConcurrent bounds simultaneous searches. A search can fetch several
	// whole segments into memory, so unlimited concurrency would let a burst
	// of queries exhaust it. Extra requests are refused at once with
	// ResourceExhausted (shedding load), rather than queued without limit.
	MaxConcurrent int
	// Timeout is applied to searches whose caller set no deadline.
	Timeout time.Duration
	// MaxOutcomes caps the per-segment list in a response. With more segments
	// than this the list is omitted (and flagged), keeping responses small; the
	// summary counts are always complete.
	MaxOutcomes int
	// Monitor, if set, feeds System with events, the ingest rate and the
	// buffer's load; without it System lists segments only.
	Monitor *activity.Monitor
	Logger  *slog.Logger
}

// Service implements stratav1.QueryServiceServer on top of an Engine.
type Service struct {
	stratav1.UnimplementedQueryServiceServer
	engine *Engine
	slots  chan struct{}
	cfg    ServiceConfig
}

// NewService wraps engine for use with gRPC.
func NewService(engine *Engine, cfg ServiceConfig) *Service {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxOutcomes <= 0 {
		cfg.MaxOutcomes = DefaultMaxOutcomes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{engine: engine, slots: make(chan struct{}, cfg.MaxConcurrent), cfg: cfg}
}

// Search implements QueryService.Search.
func (s *Service) Search(ctx context.Context, req *stratav1.SearchRequest) (*stratav1.SearchResponse, error) {
	if len(req.GetText()) > MaxTextBytes {
		return nil, status.Errorf(codes.InvalidArgument, "text is %d bytes, limit is %d", len(req.GetText()), MaxTextBytes)
	}
	if req.GetLimit() > MaxLimit {
		return nil, status.Errorf(codes.InvalidArgument, "limit %d exceeds the maximum of %d", req.GetLimit(), MaxLimit)
	}
	from, to := req.GetFromUnixNano(), req.GetToUnixNano()
	if from < 0 || to < 0 || (from != 0 && to != 0 && from >= to) {
		return nil, status.Error(codes.InvalidArgument, "time range must have 0 <= from < to")
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "too many searches running; retry shortly")
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}

	res, err := s.engine.Search(ctx, Query{
		Text: req.GetText(), Tags: req.GetTags(),
		From: from, To: to, Limit: int(req.GetLimit()), NewestFirst: req.GetNewestFirst(),
	})
	if err != nil {
		return nil, s.toStatus(ctx, err)
	}

	resp := &stratav1.SearchResponse{
		Truncated: res.Truncated,
		Metrics: &stratav1.SearchMetrics{
			SegmentsConsidered: uint32(res.Metrics.SegmentsConsidered),
			SkippedByTime:      uint32(res.Metrics.SkippedByTime),
			SkippedByBloom:     uint32(res.Metrics.SkippedByBloom),
			SegmentsScanned:    uint32(res.Metrics.SegmentsScanned),
			BytesRead:          uint64(res.Metrics.BytesRead),
			Retries:            uint32(res.Metrics.Retries),
			ServerMicros:       uint64(res.Metrics.Latency.Microseconds()),
		},
	}
	if len(res.Segments) > s.cfg.MaxOutcomes {
		resp.SegmentsTruncated = true
	} else {
		resp.Segments = make([]*stratav1.SegmentOutcome, len(res.Segments))
		for i, o := range res.Segments {
			resp.Segments[i] = &stratav1.SegmentOutcome{
				Kind: outcomeKind(o.Outcome), SegmentId: o.ID,
				MinUnixNano: o.MinTS, MaxUnixNano: o.MaxTS,
				SizeBytes: uint64(o.Size), BytesRead: uint64(o.BytesRead), Hits: uint32(o.Hits),
			}
		}
	}
	if h := res.Histogram; h != nil {
		resp.Histogram = &stratav1.Histogram{StartUnixNano: h.Start, BucketNanos: h.Width, Counts: make([]uint64, len(h.Counts))}
		for i, c := range h.Counts {
			resp.Histogram.Counts[i] = uint64(c)
		}
	}
	resp.Hits = make([]*stratav1.SearchHit, len(res.Hits))
	for i, h := range res.Hits {
		resp.Hits[i] = &stratav1.SearchHit{
			TimestampUnixNano: h.Timestamp, Message: h.Message, Tags: h.Tags, SegmentId: h.SegmentID,
		}
	}
	return resp, nil
}

// Stats implements QueryService.Stats. It reads the manifest only, so it is
// cheap and is not counted against the concurrent-search limit.
func (s *Service) Stats(ctx context.Context, _ *stratav1.StatsRequest) (*stratav1.StatsResponse, error) {
	st, err := s.engine.manifest.Stats(ctx)
	if err != nil {
		s.cfg.Logger.Error("stats failed", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	tags, err := s.engine.manifest.Tags(ctx)
	if err != nil {
		s.cfg.Logger.Error("stats failed", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	resp := &stratav1.StatsResponse{
		Segments:    uint32(st.Segments),
		Entries:     uint64(st.Entries),
		StoredBytes: uint64(st.StoredBytes),
		MinUnixNano: st.MinTS,
		MaxUnixNano: st.MaxTS,
	}
	for _, k := range tags {
		resp.Tags = append(resp.Tags, &stratav1.TagKey{Key: k.Key, Values: k.Values, Capped: k.Capped})
	}
	return resp, nil
}

// System implements QueryService.System. Like Stats it reads the manifest and
// memory only, so it is cheap and not counted against the search limit.
func (s *Service) System(ctx context.Context, req *stratav1.SystemRequest) (*stratav1.SystemResponse, error) {
	segs, err := s.engine.manifest.List(ctx, manifest.StatusActive)
	if err != nil {
		s.cfg.Logger.Error("system failed", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	resp := &stratav1.SystemResponse{SegmentsTotal: uint32(len(segs))}
	if len(segs) > MaxSystemSegments {
		segs = segs[len(segs)-MaxSystemSegments:] // the newest
	}
	for _, sg := range segs {
		resp.Segments = append(resp.Segments, &stratav1.SegmentInfo{
			SegmentId: sg.ID, SizeBytes: uint64(sg.Size), Lines: uint64(sg.Count),
			MinUnixNano: sg.MinTS, MaxUnixNano: sg.MaxTS, Generation: uint32(sg.Generation),
			CreatedUnixNano: sg.CreatedAt.UnixNano(),
		})
	}
	mon := s.cfg.Monitor
	if mon == nil {
		return resp, nil
	}
	resp.Compaction = mon.Compaction
	resp.BufferSealAfterMillis = mon.SealAfter.Milliseconds()
	if mon.Log != nil {
		resp.StartedUnixNano = mon.Log.Started().UnixNano()
		for _, e := range mon.Log.Since(req.GetEventsAfter()) {
			resp.Events = append(resp.Events, &stratav1.SystemEvent{
				Seq: e.Seq, AtUnixNano: e.At.UnixNano(), Kind: eventKind(e.Kind),
				SegmentId: e.Segment, Inputs: e.Inputs, Lines: uint64(e.Lines),
				Bytes: uint64(e.Bytes), BytesIn: uint64(e.BytesIn), Files: uint32(e.Files),
			})
		}
	}
	if mon.Rate != nil {
		resp.IngestLinesPerSecond = mon.Rate.PerSecond()
	}
	if mon.Load != nil {
		pending, sealAt, refuseAt := mon.Load()
		resp.BufferBytes, resp.BufferSealBytes, resp.BufferLimitBytes = uint64(pending), uint64(sealAt), uint64(refuseAt)
	}
	return resp, nil
}

func eventKind(k activity.Kind) stratav1.SystemEvent_Kind {
	switch k {
	case activity.Sealed:
		return stratav1.SystemEvent_KIND_SEALED
	case activity.Merged:
		return stratav1.SystemEvent_KIND_MERGED
	case activity.Cleaned:
		return stratav1.SystemEvent_KIND_CLEANED
	}
	return stratav1.SystemEvent_KIND_UNSPECIFIED
}

func outcomeKind(o Outcome) stratav1.SegmentOutcome_Kind {
	switch o {
	case SkippedByTime:
		return stratav1.SegmentOutcome_KIND_SKIPPED_BY_TIME
	case SkippedByBloom:
		return stratav1.SegmentOutcome_KIND_SKIPPED_BY_BLOOM
	case Scanned:
		return stratav1.SegmentOutcome_KIND_SCANNED
	}
	return stratav1.SegmentOutcome_KIND_UNSPECIFIED
}

// toStatus maps engine errors to gRPC codes. Damaged or missing data is
// DataLoss: the answer can't be trusted, and the message names the segment so
// an operator can find it.
func (s *Service) toStatus(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "search timed out")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "search cancelled")
	case errors.Is(err, segment.ErrCorrupt), errors.Is(err, ErrSegmentMissing):
		s.cfg.Logger.Error("search hit damaged or missing data", "err", err)
		return status.Error(codes.DataLoss, err.Error())
	default:
		s.cfg.Logger.Error("search failed", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}
