package ingest

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/segment"
)

const (
	DefaultMaxRecvMsgBytes = 4 << 20
	DefaultMaxBatchEntries = 10_000
)

// ServerConfig controls the gRPC ingest server. Zero values use defaults.
type ServerConfig struct {
	// MaxRecvMsgBytes is the largest request the server will read. gRPC
	// refuses bigger ones with ResourceExhausted before decoding them, so a
	// single huge message can't exhaust server memory.
	MaxRecvMsgBytes int
	// MaxBatchEntries caps entries per batch (a second limit, because many
	// tiny entries fit under the byte limit but still cost a lot to process).
	MaxBatchEntries int
	Logger          *slog.Logger
}

// NewGRPCServer returns a gRPC server with the ingest service registered on
// it. The caller owns the listener: call Serve(lis) to run it.
//
// Shutdown order matters for not losing data: first GracefulStop() the gRPC
// server (stop taking new batches, let in-flight ones finish), and only then
// Close() the Buffer, which seals whatever is still in memory.
func NewGRPCServer(buf *Buffer, cfg ServerConfig) *grpc.Server {
	if cfg.MaxRecvMsgBytes == 0 {
		cfg.MaxRecvMsgBytes = DefaultMaxRecvMsgBytes
	}
	if cfg.MaxBatchEntries == 0 {
		cfg.MaxBatchEntries = DefaultMaxBatchEntries
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	svc := &service{buf: buf, cfg: cfg}
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes),
		grpc.ChainStreamInterceptor(recoverInterceptor(cfg.Logger)),
		// Detect clients that vanished without closing the connection (cable
		// pulled, machine died): ping idle connections and drop the ones that
		// don't answer, so their streams don't sit open forever.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	)
	stratav1.RegisterIngestServiceServer(srv, svc)
	return srv
}

type service struct {
	stratav1.UnimplementedIngestServiceServer
	buf *Buffer
	cfg ServerConfig
}

// Ingest reads batches until the client closes its side, answering each one.
func (s *service) Ingest(stream grpc.BidiStreamingServer[stratav1.IngestRequest, stratav1.IngestResponse]) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil // client finished cleanly
		}
		if err != nil {
			// Client went away, was cancelled, or sent an oversized message.
			// gRPC already gives these a status code; pass it through.
			return err
		}
		resp, err := s.handleBatch(stream, req)
		if err != nil {
			return err
		}
		if err := stream.Send(resp); err != nil {
			// We can't tell the client what happened to this batch. It WAS
			// processed, so the client must treat an unacknowledged batch as
			// "unknown" and resend. See the failure notes.
			return err
		}
	}
}

func (s *service) handleBatch(stream grpc.BidiStreamingServer[stratav1.IngestRequest, stratav1.IngestResponse], req *stratav1.IngestRequest) (*stratav1.IngestResponse, error) {
	resp := &stratav1.IngestResponse{BatchId: req.GetBatchId()}

	if n := len(req.GetEntries()); n > s.cfg.MaxBatchEntries {
		resp.Status = stratav1.IngestStatus_INGEST_STATUS_INVALID
		resp.Message = fmt.Sprintf("batch has %d entries, limit is %d", n, s.cfg.MaxBatchEntries)
		return resp, nil
	}

	now := time.Now().UnixNano()
	entries := make([]segment.Entry, len(req.GetEntries()))
	for i, e := range req.GetEntries() {
		ts := e.GetTimestampUnixNano()
		if ts == 0 {
			ts = now // unset: use receive time
		}
		entries[i] = segment.Entry{Timestamp: ts, Message: e.GetMessage(), Tags: e.GetTags()}
	}

	err := s.buf.AddBatch(stream.Context(), entries)
	switch {
	case err == nil:
		resp.Status = stratav1.IngestStatus_INGEST_STATUS_OK
		resp.Accepted = uint32(len(entries))
	case errors.Is(err, ErrSealFailed):
		// The batch is safely in the buffer; the failure was in the background
		// work of writing a segment, which will be retried. The client must
		// not be told "failed" or it would resend and duplicate the batch.
		s.cfg.Logger.Warn("seal failed after accepting batch", "err", err)
		resp.Status = stratav1.IngestStatus_INGEST_STATUS_OK
		resp.Accepted = uint32(len(entries))
	case errors.Is(err, segment.ErrInvalidEntry):
		resp.Status = stratav1.IngestStatus_INGEST_STATUS_INVALID
		resp.Message = err.Error()
	case errors.Is(err, ErrBufferFull):
		resp.Status = stratav1.IngestStatus_INGEST_STATUS_BUFFER_FULL
		resp.Message = "server buffer is full; retry later"
	case errors.Is(err, ErrClosed):
		return nil, status.Error(codes.Unavailable, "server is shutting down")
	default:
		s.cfg.Logger.Error("unexpected ingest error", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	return resp, nil
}

// recoverInterceptor turns a panic in a handler into an Internal error for
// that one stream. Without it, a bug triggered by one malformed request would
// crash the whole process and drop every other client's connection.
func recoverInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in gRPC handler", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}
