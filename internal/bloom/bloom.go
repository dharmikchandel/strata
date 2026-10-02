// Package bloom implements a small, deterministic bloom filter.
//
// A bloom filter answers "could this term be in the set?" using a fixed
// number of bits. The answer is asymmetric, and that asymmetry is the whole
// reason it is useful for Strata:
//
//   - "no"    is always correct (no false negatives). The segment can be skipped.
//   - "maybe" may be wrong (false positives). We scan a segment needlessly,
//     which costs time but never correctness.
//
// The hash function is deliberately fixed (FNV-1a), not Go's randomly seeded
// maphash: a filter written by one process must be readable by another, since
// filters are persisted inside segment files and later in the manifest.
package bloom

import (
	"encoding/binary"
	"errors"
	"math"
)

// DefaultFalsePositiveRate is used when the caller passes an invalid rate.
const DefaultFalsePositiveRate = 0.01

const (
	minBits   = 64
	maxHashes = 64
	// headerSize is k (uint32) + m (uint64) in the serialized form.
	headerSize = 4 + 8
)

// Filter is a bloom filter. It is not safe for concurrent Add; concurrent
// MayContain calls on a filter that is no longer being modified are fine.
type Filter struct {
	bits []byte
	m    uint64 // number of bits
	k    uint32 // number of hash probes per item
}

// New sizes a filter for n expected items at the target false-positive rate p.
//
// The standard formulas: m = -n*ln(p) / (ln 2)^2 bits and k = (m/n)*ln 2
// probes. More bits per item lowers the false-positive rate; k is the
// number of probes that minimizes it for a given m/n.
func New(n int, p float64) *Filter {
	if n < 1 {
		n = 1
	}
	if !(p > 0 && p < 1) {
		p = DefaultFalsePositiveRate
	}
	m := uint64(math.Ceil(-float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)))
	if m < minBits {
		m = minBits
	}
	k := uint32(math.Round(float64(m) / float64(n) * math.Ln2))
	if k < 1 {
		k = 1
	}
	if k > maxHashes {
		k = maxHashes
	}
	return &Filter{bits: make([]byte, (m+7)/8), m: m, k: k}
}

// Add inserts a term.
func (f *Filter) Add(s string) {
	h1, h2 := hashes(s)
	for i := uint64(0); i < uint64(f.k); i++ {
		pos := (h1 + i*h2) % f.m
		f.bits[pos/8] |= 1 << (pos % 8)
	}
}

// MayContain reports whether s might have been added. A false result is
// definitive; a true result may be a false positive.
func (f *Filter) MayContain(s string) bool {
	h1, h2 := hashes(s)
	for i := uint64(0); i < uint64(f.k); i++ {
		pos := (h1 + i*h2) % f.m
		if f.bits[pos/8]&(1<<(pos%8)) == 0 {
			return false
		}
	}
	return true
}

// hashes derives two independent-ish 64-bit hashes from one FNV-1a pass.
// Probe i uses h1 + i*h2 (Kirsch–Mitzenmacher double hashing), which gives
// k probes for the price of one hash computation with no measurable loss in
// false-positive rate. h2 is forced odd so it is never zero.
func hashes(s string) (h1, h2 uint64) {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	// splitmix64 finalizer: FNV's low bits are weak for short strings, so
	// scramble them before using the value as a probe stride.
	x := h + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return h, x | 1
}

// Marshal serializes the filter as: k (uint32 LE), m (uint64 LE), bit array.
func (f *Filter) Marshal() []byte {
	out := make([]byte, headerSize+len(f.bits))
	binary.LittleEndian.PutUint32(out[0:4], f.k)
	binary.LittleEndian.PutUint64(out[4:12], f.m)
	copy(out[headerSize:], f.bits)
	return out
}

// Unmarshal parses the output of Marshal. It validates sizes so corrupt
// input returns an error rather than a filter that panics on use.
func Unmarshal(data []byte) (*Filter, error) {
	if len(data) < headerSize {
		return nil, errors.New("bloom: data too short")
	}
	k := binary.LittleEndian.Uint32(data[0:4])
	m := binary.LittleEndian.Uint64(data[4:12])
	if k < 1 || k > maxHashes {
		return nil, errors.New("bloom: invalid hash count")
	}
	if m < 1 || m > uint64(len(data)-headerSize)*8 || uint64(len(data)-headerSize) != (m+7)/8 {
		return nil, errors.New("bloom: bit array size does not match header")
	}
	bits := make([]byte, len(data)-headerSize)
	copy(bits, data[headerSize:])
	return &Filter{bits: bits, m: m, k: k}, nil
}
