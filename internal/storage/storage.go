// Package storage defines the object-store abstraction Strata uses for
// segment files, plus implementations of it.
//
// The interface is intentionally tiny and shaped like S3's object API
// (whole-object put/get, prefix listing, idempotent delete) so that a local
// disk implementation and an S3-compatible one are interchangeable.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound is returned by Get when the key does not exist.
var ErrNotFound = errors.New("storage: key not found")

// ErrInvalidKey is returned when a key cannot be safely used.
var ErrInvalidKey = errors.New("storage: invalid key")

// Storage is a flat key -> bytes store. Keys may contain "/" to look like a
// hierarchy, but there are no real directories as far as callers are concerned.
type Storage interface {
	// Put stores the contents of r under key, replacing any existing object.
	// It must be atomic: readers see either the old object or the complete
	// new one, never a partial write, and a successful Put is durable.
	Put(ctx context.Context, key string, r io.Reader) error

	// Get returns a reader for the object. The caller must Close it.
	// Returns ErrNotFound if the key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// List returns all keys that start with prefix, sorted ascending.
	// An empty prefix lists everything.
	List(ctx context.Context, prefix string) ([]string, error)

	// Delete removes the object. Deleting a missing key is not an error
	// (S3 behaves the same way), which makes retries and crash recovery simple.
	Delete(ctx context.Context, key string) error
}

// ValidateKey rejects keys that are unsafe or ambiguous ("", "..", "a//b",
// NUL or backslash characters). Every implementation applies the same rules
// so code that works against one backend works against all of them, including
// the in-memory fake used in tests.
func ValidateKey(key string) error {
	if key == "" || strings.ContainsRune(key, 0) || strings.ContainsRune(key, '\\') {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}
