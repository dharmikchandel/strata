package storage

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// A server that accepts connections but never answers must not hang the
// caller: the per-operation timeout has to fire. This is the "hung Put blocks
// Close" failure from the Phase 2 notes. It needs no real S3 server.
func TestS3OperationsTimeOutAgainstHungServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it open, say nothing
		}
	}()

	s, err := NewS3(context.Background(), S3Config{
		Endpoint:  "http://" + ln.Addr().String(),
		Bucket:    "b",
		AccessKey: "k",
		SecretKey: "s",
		PathStyle: true,
		OpTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = s.Put(context.Background(), "k", strings.NewReader("x"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Put took %v; timeout did not bound it", elapsed)
	}
	if _, err := s.Get(context.Background(), "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get: want deadline exceeded, got %v", err)
	}
	if _, err := s.List(context.Background(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("list: want deadline exceeded, got %v", err)
	}
}

func TestNewS3RequiresBucket(t *testing.T) {
	if _, err := NewS3(context.Background(), S3Config{}); err == nil {
		t.Fatal("expected error")
	}
}
