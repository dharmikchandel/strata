package query

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
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
	Logger      *slog.Logger
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
		From: from, To: to, Limit: int(req.GetLimit()),
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
	return &stratav1.StatsResponse{
		Segments:    uint32(st.Segments),
		Entries:     uint64(st.Entries),
		StoredBytes: uint64(st.StoredBytes),
		MinUnixNano: st.MinTS,
		MaxUnixNano: st.MaxTS,
	}, nil
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
