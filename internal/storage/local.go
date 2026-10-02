package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// tmpPrefix marks in-flight Put files. Keys may not use it, and List hides it,
// so a crash mid-Put leaves garbage that is invisible to callers.
const tmpPrefix = ".tmp-"

// Local stores each object as a regular file under a root directory.
type Local struct {
	root string
}

var _ Storage = (*Local)(nil)

// NewLocal returns a Local store rooted at dir, creating it if needed.
func NewLocal(dir string) (*Local, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &Local{root: dir}, nil
}

// path validates key and maps it to a file path under root.
//
// Keys come from the rest of the system, but they will eventually be built
// from data we do not fully control, so we reject anything that could escape
// the root ("..", absolute paths) rather than relying on callers.
func (l *Local) path(key string) (string, error) {
	if key == "" || strings.ContainsRune(key, 0) || strings.ContainsRune(key, '\\') {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, tmpPrefix) {
			return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

// Put writes atomically using the classic temp-file + rename pattern:
//
//  1. write the full contents to a temp file in the same directory
//  2. fsync the file so the bytes are on disk, not just in the page cache
//  3. rename over the final name (atomic on POSIX filesystems)
//  4. fsync the directory so the rename itself survives a power loss
//
// A crash at any point leaves either no object or the complete object, never
// a half-written segment. The temp file must be in the same directory (same
// filesystem) because rename is only atomic within one filesystem.
func (l *Local) Put(ctx context.Context, key string, r io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, r); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("storage: put %q: sync: %w", key, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	if err = os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	// CreateTemp makes files 0600; objects should look like normal files.
	if err = os.Chmod(p, 0o644); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	if err = syncDir(dir); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Get opens the object for reading.
func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get %q: %w", key, err)
	}
	// A key like "a" can name a directory when "a/b" exists; that is not an object.
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, ErrNotFound
	}
	return f, nil
}

// List walks the root and returns matching keys in sorted order.
func (l *Local) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		rel, err := filepath.Rel(l.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: list %q: %w", prefix, err)
	}
	// WalkDir is lexical per directory, which is not the same as sorting the
	// full keys ("a.txt" < "a/b" but the walk visits "a/" first).
	sort.Strings(keys)
	return keys, nil
}

// Delete removes the object; a missing key is not an error.
func (l *Local) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}
