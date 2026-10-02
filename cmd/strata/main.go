// Command strata runs the Strata log search server: it accepts logs over gRPC,
// stores them as immutable segments in S3-compatible object storage, and keeps
// them compacted in the background.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dharmikchandel/strata/internal/app"
)

// shutdownTimeout bounds the whole graceful shutdown, including the final
// flush of buffered logs to storage.
const shutdownTimeout = 30 * time.Second

func main() {
	err := run()
	var help *app.HelpRequested
	if errors.As(err, &help) {
		fmt.Print(help.Usage)
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "strata:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := app.ParseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}

	// SIGINT (Ctrl-C) and SIGTERM (what Docker and Kubernetes send) start a
	// graceful shutdown. After the first signal, stop() restores the default
	// behaviour, so a second Ctrl-C kills the process immediately.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := app.Start(ctx, cfg)
	if err != nil {
		return err
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-srv.Done():
		if serveErr == nil {
			serveErr = errors.New("gRPC server stopped unexpectedly")
		}
	}
	stop()

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(serveErr, srv.Shutdown(sctx))
}
