package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/dharmikchandel/strata/internal/compact"
	"github.com/dharmikchandel/strata/internal/ingest"
	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/storage"
)

// App is a running Strata server.
type App struct {
	cfg Config
	log *slog.Logger

	manifest *manifest.Manifest
	store    storage.Storage
	buffer   *ingest.Buffer
	grpc     *grpc.Server
	health   *health.Server
	lis      net.Listener

	cancelCompactor context.CancelFunc
	compactorDone   chan struct{}
	serveDone       chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

// Start connects to storage, opens the manifest, and begins serving. If any
// step fails, everything opened so far is released and the error says what
// went wrong, so a bad configuration fails at startup and not under load.
func Start(ctx context.Context, cfg Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, log: cfg.logger()}
	ok := false
	defer func() {
		if !ok {
			a.release()
		}
	}()

	// 1. Storage, checked now so a wrong endpoint, bucket or password is
	// reported immediately.
	if cfg.Storage != nil {
		a.store = cfg.Storage
	} else {
		s3, err := storage.NewS3(ctx, cfg.S3)
		if err != nil {
			return nil, err
		}
		if cfg.CreateBucket {
			err = s3.EnsureBucket(ctx)
		} else {
			err = s3.CheckBucket(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("startup: %w", err)
		}
		a.store = s3
	}

	// 2. Manifest.
	if dir := filepath.Dir(cfg.ManifestPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("startup: create manifest directory: %w", err)
		}
	}
	m, err := manifest.Open(cfg.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("startup: %w", err)
	}
	a.manifest = m

	// 2b. Make sure this manifest and this bucket belong together.
	if err := checkIdentity(ctx, m, a.store, cfg.AdoptBucket, a.log); err != nil {
		return nil, fmt.Errorf("startup: %w", err)
	}

	// 3. Ingest buffer. A sealed segment is recorded in the manifest as part
	// of sealing: if recording fails the buffer deletes the file and keeps the
	// entries (see ingest.Config.OnSeal).
	buf, err := ingest.NewBuffer(ingest.Config{
		Storage:  a.store,
		MaxBytes: cfg.BufferBytes,
		MaxAge:   cfg.BufferAge,
		OnSeal: func(ss ingest.SealedSegment) error {
			return m.AddSegment(context.Background(), manifest.Segment{
				ID: ss.ID, Key: ss.Key, MinTS: ss.MinTS, MaxTS: ss.MaxTS,
				Count: ss.Count, Size: ss.Size, Bloom: ss.Bloom,
			})
		},
		OnError: func(err error) { a.log.Error("background seal failed", "err", err) },
	})
	if err != nil {
		return nil, fmt.Errorf("startup: %w", err)
	}
	a.buffer = buf

	// 4. gRPC server.
	a.lis, err = net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("startup: listen on %s: %w", cfg.ListenAddr, err)
	}
	a.grpc = ingest.NewGRPCServer(buf, ingest.ServerConfig{MaxRecvMsgBytes: cfg.MaxRecvMsgBytes, Logger: a.log})
	// The standard gRPC health service lets load balancers and container
	// healthchecks ask "is this instance serving?", and lets us answer "no"
	// while draining at shutdown.
	a.health = health.NewServer()
	healthpb.RegisterHealthServer(a.grpc, a.health)
	a.serveDone = make(chan error, 1)
	go func() { a.serveDone <- a.grpc.Serve(a.lis) }()
	a.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	// 5. Background compaction and garbage collection.
	if cfg.CompactEnabled {
		comp, err := compact.New(compact.Config{
			Manifest:    m,
			Storage:     a.store,
			SmallBytes:  cfg.CompactSmallBytes,
			TargetBytes: cfg.CompactTargetBytes,
			Interval:    cfg.CompactInterval,
			OrphanGrace: cfg.OrphanGrace,
			Logger:      a.log,
		})
		if err != nil {
			return nil, fmt.Errorf("startup: %w", err)
		}
		cctx, cancel := context.WithCancel(context.Background())
		a.cancelCompactor = cancel
		a.compactorDone = make(chan struct{})
		go func() { defer close(a.compactorDone); comp.Run(cctx) }()
	}

	if host, _, err := net.SplitHostPort(a.lis.Addr().String()); err == nil {
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			a.log.Warn("listening on a non-loopback address without TLS or authentication; anyone who can reach it can write logs", "addr", a.lis.Addr().String())
		}
	}
	a.log.Info("strata started", "listen", a.lis.Addr().String(), "bucket", cfg.S3.Bucket, "manifest", cfg.ManifestPath, "compaction", cfg.CompactEnabled)
	ok = true
	return a, nil
}

// Addr is the address the gRPC server is listening on.
func (a *App) Addr() net.Addr { return a.lis.Addr() }

// Done receives an error if the gRPC server stops by itself.
func (a *App) Done() <-chan error { return a.serveDone }

// Shutdown stops the server without losing buffered data, in this order:
//
//  1. Mark the health check NOT_SERVING, so load balancers stop sending traffic.
//  2. Stop the gRPC server: no new streams, and in-flight ones get up to
//     ShutdownGrace to finish, after which they are cut off. (Cutting them off
//     is safe: a batch whose reply never arrived is "unknown" to the client,
//     which must resend, and acknowledged batches are in the buffer.)
//  3. Close the buffer, which seals everything still in memory into a final
//     segment. This must come AFTER step 2, otherwise batches that arrive
//     during the drain would be refused.
//  4. Stop the compactor.
//  5. Close the manifest, last, because steps 3 and 4 still write to it.
//
// ctx bounds the whole thing. If the final flush fails (storage down), the
// error says how many entries were lost.
func (a *App) Shutdown(ctx context.Context) error {
	a.shutdownOnce.Do(func() { a.shutdownErr = a.shutdown(ctx) })
	return a.shutdownErr
}

func (a *App) shutdown(ctx context.Context) error {
	var errs []error
	a.log.Info("shutting down")
	a.health.Shutdown() // NOT_SERVING, and stays so

	stopped := make(chan struct{})
	go func() { a.grpc.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(a.cfg.ShutdownGrace):
		a.log.Warn("in-flight streams did not finish in time; cutting them off", "grace", a.cfg.ShutdownGrace)
		a.grpc.Stop()
		<-stopped
	case <-ctx.Done():
		a.grpc.Stop()
		<-stopped
	}

	pending := a.buffer.Pending()
	if err := a.buffer.Close(ctx); err != nil {
		lost := a.buffer.Pending()
		a.log.Error("final flush failed; buffered entries are lost", "pending_before", pending, "lost", lost, "err", err)
		errs = append(errs, fmt.Errorf("final flush failed, %d buffered entries lost: %w", lost, err))
	}

	if a.cancelCompactor != nil {
		a.cancelCompactor()
		select {
		case <-a.compactorDone:
		case <-ctx.Done():
			errs = append(errs, errors.New("compactor did not stop before the deadline"))
		}
	}
	if err := a.manifest.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close manifest: %w", err))
	}
	a.log.Info("stopped")
	return errors.Join(errs...)
}

// release undoes a partly finished Start.
func (a *App) release() {
	if a.cancelCompactor != nil {
		a.cancelCompactor()
		<-a.compactorDone
	}
	if a.grpc != nil {
		a.grpc.Stop()
	} else if a.lis != nil {
		a.lis.Close()
	}
	if a.buffer != nil {
		a.buffer.Close(context.Background())
	}
	if a.manifest != nil {
		a.manifest.Close()
	}
}
