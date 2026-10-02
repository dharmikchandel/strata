package storage_test

import (
	"testing"

	"github.com/dharmikchandel/strata/internal/storage"
	"github.com/dharmikchandel/strata/internal/storage/storagetest"
)

// The in-memory fake used by other packages' tests must behave like S3, so
// it has to pass the same suite.
func TestMemContract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Storage { return storagetest.NewMem() })
}

// Needs the S3 server from docker-compose.yml; skipped when it isn't running.
func TestS3Contract(t *testing.T) {
	storagetest.Run(t, storagetest.NewS3)
}
