package segment

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// KeyPrefix is where segment files live in object storage.
	KeyPrefix = "segments/"
	keySuffix = ".strata"
)

// NewID returns a unique segment ID: the creation time in nanoseconds (hex,
// fixed width, so IDs sort by age) plus random bytes so two segments created
// in the same nanosecond, or by two processes, never collide.
//
// Embedding the time is deliberate: garbage collection has to tell "a leftover
// file from a crash" apart from "a segment being written right now, not yet
// recorded in the manifest". The storage interface has no modification time,
// so the age comes from the ID.
func NewID() string {
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		panic(err) // crypto/rand failing means the OS is broken
	}
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(r[:]))
}

// KeyForID returns the storage key of a segment.
func KeyForID(id string) string { return KeyPrefix + id + keySuffix }

// CreatedAt extracts the creation time from a storage key made by KeyForID.
// ok is false for keys that don't follow the naming scheme (foreign objects),
// which callers must leave alone.
func CreatedAt(key string) (t time.Time, ok bool) {
	if !strings.HasPrefix(key, KeyPrefix) || !strings.HasSuffix(key, keySuffix) {
		return time.Time{}, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, KeyPrefix), keySuffix)
	ts, _, found := strings.Cut(id, "-")
	if !found || len(ts) != 16 {
		return time.Time{}, false
	}
	n, err := strconv.ParseUint(ts, 16, 63)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, int64(n)), true
}
