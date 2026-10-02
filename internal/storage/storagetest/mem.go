package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/dharmikchandel/strata/internal/storage"
)

// ErrInjected is the error returned by injected faults.
var ErrInjected = errors.New("storagetest: injected fault")

// Mem is an in-memory storage.Storage for tests. It follows the same
// contract as the S3 backend (and is checked by the same contract suite), and
// can be told to fail on demand, which neither a real disk nor a real S3
// server can do predictably.
//
// It is safe for concurrent use.
type Mem struct {
	mu      sync.Mutex
	objects map[string][]byte

	failPuts        int // next N Puts: fail, store nothing
	failPutsStoring int // next N Puts: store the object, but still return an error
	failDeletes     int // next N Deletes: fail, delete nothing
}

var _ storage.Storage = (*Mem)(nil)

func NewMem() *Mem { return &Mem{objects: map[string][]byte{}} }

// FailNextPuts makes the next n Puts return ErrInjected without storing
// anything. Models "storage is down" or "disk full".
func (m *Mem) FailNextPuts(n int) { m.mu.Lock(); m.failPuts = n; m.mu.Unlock() }

// FailNextPutsAfterStoring makes the next n Puts store the object and then
// return ErrInjected anyway. Models the ambiguous failure where a network
// timeout hides the fact that the upload actually succeeded.
func (m *Mem) FailNextPutsAfterStoring(n int) { m.mu.Lock(); m.failPutsStoring = n; m.mu.Unlock() }

// FailNextDeletes makes the next n Deletes return ErrInjected without deleting.
func (m *Mem) FailNextDeletes(n int) { m.mu.Lock(); m.failDeletes = n; m.mu.Unlock() }

func (m *Mem) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	// Read outside the lock and before storing: a reader that fails halfway
	// must leave any existing object untouched.
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPuts > 0 {
		m.failPuts--
		return ErrInjected
	}
	m.objects[key] = data
	if m.failPutsStoring > 0 {
		m.failPutsStoring--
		return ErrInjected
	}
	return nil
}

func (m *Mem) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *Mem) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *Mem) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failDeletes > 0 {
		m.failDeletes--
		return ErrInjected
	}
	delete(m.objects, key)
	return nil
}
