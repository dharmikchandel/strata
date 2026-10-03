package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	stratav1 "github.com/dharmikchandel/strata/gen/strata/v1"
	"github.com/dharmikchandel/strata/internal/bench"
)

func dial(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7070", "server address")
	file := fs.String("file", "", "read from this file instead of stdin")
	format := fs.String("format", "plain", `"plain" (each line is a message, stamped with the current time) or "bgl" (BlueGene/L dataset lines)`)
	batch := fs.Int("batch", 500, "lines per batch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "plain" && *format != "bgl" {
		return fmt.Errorf("unknown -format %q", *format)
	}
	if *batch <= 0 {
		return errors.New("-batch must be positive")
	}

	var in io.Reader = os.Stdin
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	conn, err := dial(*addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	st, err := stratav1.NewIngestServiceClient(conn).Ingest(ctx)
	if err != nil {
		return err
	}

	var sent uint64
	var batchID uint64
	flush := func(entries []*stratav1.LogEntry) error {
		batchID++
		if err := st.Send(&stratav1.IngestRequest{BatchId: batchID, Entries: entries}); err != nil {
			return fmt.Errorf("send: %w", err)
		}
		resp, err := st.Recv()
		if err != nil {
			return fmt.Errorf("no reply for batch %d (it may or may not have been stored): %w", batchID, err)
		}
		if resp.Status != stratav1.IngestStatus_INGEST_STATUS_OK {
			return fmt.Errorf("batch %d rejected: %s %s (%d lines sent successfully before it)", batchID, resp.Status, resp.Message, sent)
		}
		sent += uint64(resp.Accepted)
		return nil
	}

	start := time.Now()
	var entries []*stratav1.LogEntry
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e *stratav1.LogEntry
		if *format == "bgl" {
			e, err = bench.ParseBGLLine(sc.Text())
			if err != nil {
				continue // skip malformed dataset lines
			}
		} else {
			e = &stratav1.LogEntry{Message: sc.Text()} // timestamp 0: server stamps it
		}
		entries = append(entries, e)
		if len(entries) == *batch {
			if err := flush(entries); err != nil {
				return err
			}
			entries = nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(entries) > 0 {
		if err := flush(entries); err != nil {
			return err
		}
	}
	st.CloseSend()
	took := time.Since(start)
	fmt.Printf("sent %d lines in %s (%.0f lines/s). They are searchable once the server seals them (within a few seconds).\n",
		sent, took.Round(time.Millisecond), float64(sent)/took.Seconds())
	return nil
}
