// Package storagetest holds tests that every storage.Storage implementation
// must pass, plus a helper for tests that need a real S3-compatible server.
//
// Running the same suite against the in-memory fake and S3 is what proves the interface
// is honest: code written against it behaves the same on either backend.
package storagetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dharmikchandel/strata/internal/storage"
)

// Factory returns a fresh, empty store for one test.
type Factory func(t *testing.T) storage.Storage

// NewS3 returns a store backed by a fresh, uniquely named bucket on the
// S3-compatible server described by these environment variables (defaults
// match docker-compose.yml):
//
//	STRATA_S3_ENDPOINT   default http://localhost:9000
//	STRATA_S3_ACCESS_KEY default strata-dev
//	STRATA_S3_SECRET_KEY default strata-dev-secret
//
// The test is skipped if the server isn't reachable, so `go test ./...` still
// works without Docker. The bucket and its objects are deleted afterwards.
func NewS3(t *testing.T) storage.Storage {
	t.Helper()
	ctx := context.Background()
	endpoint := env("STRATA_S3_ENDPOINT", "http://localhost:9000")
	// A plain TCP check first: the SDK retries refused connections for
	// seconds, which would make every skipped test slow.
	if u, err := url.Parse(endpoint); err != nil {
		t.Fatalf("bad STRATA_S3_ENDPOINT: %v", err)
	} else if c, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond); err != nil {
		t.Skipf("S3 server not available at %s (run `docker compose up -d`): %v", endpoint, err)
	} else {
		c.Close()
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewS3(ctx, storage.S3Config{
		Endpoint:  endpoint,
		Bucket:    "strata-test-" + hex.EncodeToString(suffix[:]),
		AccessKey: env("STRATA_S3_ACCESS_KEY", "strata-dev"),
		SecretKey: env("STRATA_S3_SECRET_KEY", "strata-dev-secret"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBucket(ctx); err != nil {
		t.Skipf("S3 server not available (run `docker compose up -d`): %v", err)
	}
	t.Cleanup(func() {
		keys, _ := s.List(ctx, "")
		for _, k := range keys {
			_ = s.Delete(ctx, k)
		}
		_ = s.DeleteBucket(ctx)
	})
	return s
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func read(t *testing.T, s storage.Storage, key string) []byte {
	t.Helper()
	r, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// failingReader yields some bytes and then an error, simulating a producer
// that dies mid-upload.
type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("boom")
}

// Run executes the contract against stores produced by newStore.
func Run(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("PutGetRoundTrip", func(t *testing.T) {
		s := newStore(t)
		want := bytes.Repeat([]byte("segment-bytes\x00\xff"), 1000)
		if err := s.Put(ctx, "segments/seg-1.strata", bytes.NewReader(want)); err != nil {
			t.Fatal(err)
		}
		if got := read(t, s, "segments/seg-1.strata"); !bytes.Equal(got, want) {
			t.Fatal("content changed on the round trip")
		}
	})

	t.Run("EmptyObject", func(t *testing.T) {
		s := newStore(t)
		if err := s.Put(ctx, "empty", strings.NewReader("")); err != nil {
			t.Fatal(err)
		}
		if got := read(t, s, "empty"); len(got) != 0 {
			t.Fatalf("got %d bytes", len(got))
		}
	})

	t.Run("PutOverwrites", func(t *testing.T) {
		s := newStore(t)
		s.Put(ctx, "k", strings.NewReader("one"))
		s.Put(ctx, "k", strings.NewReader("two"))
		if got := read(t, s, "k"); string(got) != "two" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("GetMissing", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("GetPrefixOfKeyIsNotFound", func(t *testing.T) {
		s := newStore(t)
		s.Put(ctx, "a/b", strings.NewReader("x"))
		if _, err := s.Get(ctx, "a"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("ListPrefixAndOrder", func(t *testing.T) {
		s := newStore(t)
		for _, k := range []string{"seg/b", "seg/a", "seg/sub/c", "other/x", "seg.txt"} {
			if err := s.Put(ctx, k, strings.NewReader("x")); err != nil {
				t.Fatal(err)
			}
		}
		all, err := s.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"other/x", "seg.txt", "seg/a", "seg/b", "seg/sub/c"}; !reflect.DeepEqual(all, want) {
			t.Fatalf("all: got %v want %v", all, want)
		}
		seg, _ := s.List(ctx, "seg/")
		if want := []string{"seg/a", "seg/b", "seg/sub/c"}; !reflect.DeepEqual(seg, want) {
			t.Fatalf("seg/: got %v want %v", seg, want)
		}
		if none, _ := s.List(ctx, "zzz"); len(none) != 0 {
			t.Fatalf("expected empty, got %v", none)
		}
	})

	t.Run("ListMoreThanOnePage", func(t *testing.T) {
		// S3 returns at most 1000 keys per request; make sure we follow pages.
		if testing.Short() {
			t.Skip("slow")
		}
		s := newStore(t)
		const n = 1100
		for i := 0; i < n; i++ {
			if err := s.Put(ctx, fmt.Sprintf("many/%04d", i), strings.NewReader("x")); err != nil {
				t.Fatal(err)
			}
		}
		keys, err := s.List(ctx, "many/")
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != n {
			t.Fatalf("listed %d of %d keys", len(keys), n)
		}
	})

	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		s := newStore(t)
		s.Put(ctx, "k", strings.NewReader("x"))
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatalf("second delete should succeed: %v", err)
		}
		if _, err := s.Get(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("InvalidKeys", func(t *testing.T) {
		s := newStore(t)
		bad := []string{"", "/abs", "../escape", "a/../../escape", "a//b", "a/./b", "a/", "a\\b", "a\x00b"}
		for _, k := range bad {
			if err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("put %q: want ErrInvalidKey, got %v", k, err)
			}
			if _, err := s.Get(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("get %q: want ErrInvalidKey, got %v", k, err)
			}
			if err := s.Delete(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("delete %q: want ErrInvalidKey, got %v", k, err)
			}
		}
	})

	t.Run("FailedPutLeavesOldObjectIntact", func(t *testing.T) {
		s := newStore(t)
		s.Put(ctx, "k", strings.NewReader("original"))
		if err := s.Put(ctx, "k", &failingReader{}); err == nil {
			t.Fatal("expected error")
		}
		if got := read(t, s, "k"); string(got) != "original" {
			t.Fatalf("object was corrupted: %q", got)
		}
	})

	t.Run("CancelledContext", func(t *testing.T) {
		s := newStore(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.Put(cctx, "k", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
			t.Fatalf("put: want context.Canceled, got %v", err)
		}
		if _, err := s.Get(cctx, "k"); !errors.Is(err, context.Canceled) {
			t.Fatalf("get: want context.Canceled, got %v", err)
		}
	})
}
