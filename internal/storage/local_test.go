package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Local, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func get(t *testing.T, s Storage, key string) []byte {
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

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	want := []byte("hello segment")
	if err := s.Put(ctx, "segments/seg-1.seg", bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	if got := get(t, s, "segments/seg-1.seg"); !bytes.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPutOverwrites(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.Put(ctx, "k", strings.NewReader("one"))
	s.Put(ctx, "k", strings.NewReader("two"))
	if got := get(t, s, "k"); string(got) != "two" {
		t.Fatalf("got %q", got)
	}
}

func TestGetMissing(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetDirectoryIsNotFound(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.Put(ctx, "a/b", strings.NewReader("x"))
	if _, err := s.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListPrefixAndOrder(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
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
	none, _ := s.List(ctx, "zzz")
	if len(none) != 0 {
		t.Fatalf("expected empty, got %v", none)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.Put(ctx, "k", strings.NewReader("x"))
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("second delete should succeed: %v", err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestInvalidKeys(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	bad := []string{"", "/abs", "../escape", "a/../../escape", "a//b", "a/./b", "a/", ".tmp-x", "d/.tmp-y", "a\\b", "a\x00b"}
	for _, k := range bad {
		if err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("put %q: want ErrInvalidKey, got %v", k, err)
		}
		if _, err := s.Get(ctx, k); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("get %q: want ErrInvalidKey, got %v", k, err)
		}
		if err := s.Delete(ctx, k); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("delete %q: want ErrInvalidKey, got %v", k, err)
		}
	}
}

// failingReader yields some bytes and then an error, simulating a writer
// that dies mid-upload.
type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("boom")
}

func TestFailedPutLeavesNothingBehind(t *testing.T) {
	s, dir := newStore(t)
	ctx := context.Background()
	s.Put(ctx, "k", strings.NewReader("original"))

	if err := s.Put(ctx, "k", &failingReader{}); err == nil {
		t.Fatal("expected error")
	}
	// The old object is intact: no partial overwrite.
	if got := get(t, s, "k"); string(got) != "original" {
		t.Fatalf("object was corrupted: %q", got)
	}
	// And no temp files are left lying around.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestListHidesTempFiles(t *testing.T) {
	s, dir := newStore(t)
	ctx := context.Background()
	s.Put(ctx, "real", strings.NewReader("x"))
	// Simulate a crash that left a temp file behind.
	if err := os.WriteFile(filepath.Join(dir, tmpPrefix+"123"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.List(ctx, "")
	if !reflect.DeepEqual(keys, []string{"real"}) {
		t.Fatalf("got %v", keys)
	}
}

func TestCancelledContext(t *testing.T) {
	s, _ := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Put(ctx, "k", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
