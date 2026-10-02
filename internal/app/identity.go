package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/dharmikchandel/strata/internal/manifest"
	"github.com/dharmikchandel/strata/internal/segment"
	"github.com/dharmikchandel/strata/internal/storage"
)

// identityKey is the marker object in the bucket. It sits outside the
// segments/ prefix, so listing, compaction and garbage collection never see it.
const identityKey = "strata-instance"

// checkIdentity refuses to start when the manifest file and the bucket don't
// belong together.
//
// The danger it prevents: the manifest is the only record of which segments are
// real. If the manifest file is lost (a container restarted without its
// volume, a wrong path, a deleted file) and Strata starts with a fresh one, the
// bucket still holds all the old segments, but none of them is in the new
// manifest. Garbage collection would see every one of them as an orphan and
// delete it once it is older than the grace period: the real data, destroyed
// by the cleanup job.
//
// To catch that, the manifest has a random identity, and the same identity is
// stored in the bucket as a marker object:
//
//   - marker matches the manifest: all is well.
//   - marker belongs to a different manifest: refuse to start, unless adopt is set.
//   - no marker: a new bucket (write the marker), or an older deployment that
//     predates the marker (write it too), but NOT a bucket that already holds
//     segments while the manifest has none, which looks exactly like a lost
//     manifest.
func checkIdentity(ctx context.Context, m *manifest.Manifest, store storage.Storage, adopt bool, log *slog.Logger) error {
	id, _, err := m.InstanceID(ctx)
	if err != nil {
		return err
	}
	marker, found, err := readMarker(ctx, store)
	if err != nil {
		return err
	}
	rows, err := m.CountAll(ctx)
	if err != nil {
		return err
	}

	switch {
	case found && marker == id:
		return nil

	case found:
		if !adopt {
			return fmt.Errorf("the bucket belongs to a different manifest (bucket marker %s, this manifest %s). "+
				"The manifest file was probably lost or replaced: restore it from a backup. "+
				"Starting anyway would let garbage collection delete every segment in the bucket. "+
				"If you really want to abandon the existing data, start with -adopt-bucket", marker, id)
		}
		log.Warn("adopting a bucket that belonged to a different manifest; its existing segments will be deleted as orphans",
			"old_marker", marker, "new_marker", id)

	default: // no marker
		keys, err := store.List(ctx, segment.KeyPrefix)
		if err != nil {
			return fmt.Errorf("list bucket: %w", err)
		}
		switch {
		case len(keys) > 0 && rows == 0 && !adopt:
			return fmt.Errorf("the bucket already holds %d segment files but this manifest has none. "+
				"The manifest file was probably lost: restore it from a backup. "+
				"If you really want to abandon the existing data, start with -adopt-bucket", len(keys))
		case len(keys) == 0 && rows > 0:
			log.Warn("the manifest lists segments but the bucket is empty; was the bucket recreated or wiped?", "segments", rows)
		}
	}

	if err := store.Put(ctx, identityKey, strings.NewReader(id)); err != nil {
		return fmt.Errorf("write bucket marker: %w", err)
	}
	return nil
}

func readMarker(ctx context.Context, store storage.Storage) (id string, found bool, err error) {
	r, err := store.Get(ctx, identityKey)
	if errors.Is(err, storage.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read bucket marker: %w", err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, 1024))
	if err != nil {
		return "", false, fmt.Errorf("read bucket marker: %w", err)
	}
	return string(bytes.TrimSpace(data)), true, nil
}
