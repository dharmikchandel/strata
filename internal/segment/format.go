// Package segment defines Strata's immutable on-disk segment format.
//
// A segment is one self-contained file holding a batch of log entries plus
// everything needed to search them without reading anything else:
//
//	+---------------------------+
//	| header (fixed size)       |  magic, version, count, min/max timestamp,
//	|                           |  section offsets, checksums
//	+---------------------------+
//	| lines section             |  offset table + length-prefixed records
//	+---------------------------+
//	| index section             |  inverted index: term -> line ids
//	+---------------------------+
//	| bloom section             |  bloom filter over every term in the index
//	+---------------------------+
//
// Segments are written once and never modified. That immutability is what
// makes the rest of the design simple: no locking on read, no in-place index
// updates, and a segment can be cached or copied anywhere without worrying
// that it will change underneath you.
package segment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	magic   = "STRA"
	version = 1
)

// ErrCorrupt is wrapped by every error caused by malformed segment bytes.
var ErrCorrupt = errors.New("segment: corrupt")

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// header is the fixed-size prefix of every segment. All integers are
// little-endian. Offsets are absolute positions in the file.
//
// The min/max timestamps and count live here (and not only inside the lines
// section) because the manifest and query planner want them without parsing
// the rest of the file.
type header struct {
	Magic   [4]byte
	Version uint16
	Flags   uint16 // reserved, must be zero in version 1
	Count   uint32
	MinTS   int64
	MaxTS   int64

	LinesOff, LinesLen uint64
	IndexOff, IndexLen uint64
	BloomOff, BloomLen uint64

	// BodyCRC covers every byte after the header; HeaderCRC covers every
	// header byte before itself. Two checksums let us tell "header damaged"
	// from "body damaged" and let Decode reject bad data before trusting any
	// of the offsets above.
	BodyCRC   uint32
	HeaderCRC uint32
}

var headerSize = binary.Size(header{})

func (h *header) marshal() []byte {
	var buf bytes.Buffer
	// Writing a fixed-size struct to a bytes.Buffer cannot fail.
	_ = binary.Write(&buf, binary.LittleEndian, h)
	b := buf.Bytes()
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	return b
}

func parseHeader(data []byte) (header, error) {
	var h header
	if len(data) < headerSize {
		return h, corrupt("file too short for header (%d bytes)", len(data))
	}
	raw := data[:headerSize]
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &h); err != nil {
		return h, corrupt("header: %v", err)
	}
	if string(h.Magic[:]) != magic {
		return h, corrupt("bad magic %q", h.Magic[:])
	}
	if want := crc32.ChecksumIEEE(raw[:headerSize-4]); h.HeaderCRC != want {
		return h, corrupt("header checksum mismatch")
	}
	if h.Version != version {
		return h, fmt.Errorf("segment: unsupported version %d", h.Version)
	}
	if h.Flags != 0 {
		return h, corrupt("unknown flags %#x", h.Flags)
	}
	if h.Count == 0 {
		return h, corrupt("segment has no entries")
	}
	if h.MinTS > h.MaxTS {
		return h, corrupt("min timestamp after max timestamp")
	}
	return h, nil
}

// cursor reads from a byte slice and remembers the first error, so decoding
// code can read several fields and check err once. Every read is bounds
// checked: corrupt input must produce an error, never a panic or a huge
// allocation.
type cursor struct {
	b   []byte
	err error
}

func (c *cursor) fail(msg string) {
	if c.err == nil {
		c.err = corrupt("%s", msg)
	}
}

func (c *cursor) uvarint() uint64 {
	if c.err != nil {
		return 0
	}
	v, n := binary.Uvarint(c.b)
	if n <= 0 {
		c.fail("bad varint")
		return 0
	}
	c.b = c.b[n:]
	return v
}

func (c *cursor) bytes(n uint64) []byte {
	if c.err != nil {
		return nil
	}
	if n > uint64(len(c.b)) {
		c.fail("length exceeds data")
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) int64() int64 {
	b := c.bytes(8)
	if b == nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}
