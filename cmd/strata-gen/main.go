// Command strata-gen sends a steady stream of realistic, made-up log lines to a
// running Strata server, and (for a run with a fixed duration) checks afterwards
// that every line the server acknowledged can be found.
//
//	strata-gen -profile busy                       # run until interrupted
//	strata-gen -profile busy -duration 2m          # a test: send, then verify
//	strata-gen -rate 300 -late-fraction 0.1 -late-max 1m
//
// The lines are modelled on the real BGL supercomputer log: the message shapes
// and their frequencies were derived from it. They are made up, not real logs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dharmikchandel/strata/internal/gen"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := gen.ParseConfig(os.Args[1:], os.Getenv)
	var help *gen.HelpRequested
	if errors.As(err, &help) {
		fmt.Print(help.Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "strata-gen:", err)
		return 1
	}
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Ctrl-C / SIGTERM stop the generating; lines already due are still delivered.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := gen.Run(ctx, cfg)
	if rep != nil {
		fmt.Println(rep)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "strata-gen:", err)
		return 1
	}
	if cfg.FailOnLoss && rep.Verify != nil && rep.Verify.Lost > 0 {
		return 2
	}
	return 0
}
