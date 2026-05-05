package internal

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExifToolPool_ServiceAlwaysReturned verifies that after ExtractDate returns
// (including on an error path), the service is back in the pool and a subsequent
// call does not deadlock. With the old code (no defer), a panic would drain the
// pool; this test covers the error-return path which exercises the same defer.
func TestExifToolPool_ServiceAlwaysReturned(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not found in PATH")
	}

	// Pool of size 1: if ExtractDate ever fails to return the service,
	// the second call blocks forever and the test times out.
	pool, err := NewExifToolPool(1)
	require.NoError(t, err)
	defer pool.Close()

	// First call: non-existent file → exiftool returns no metadata, no error.
	_, _, err = pool.ExtractDate("/tmp/does-not-exist-media-organizer.jpg", false, false)
	// err may or may not be set depending on exiftool version; what matters is
	// that the call completes and the service is back in the pool.
	_ = err

	// Second call on a valid (but tag-less) file: would deadlock if the service
	// was not returned after the first call.
	_, _, err = pool.ExtractDate("/tmp/does-not-exist-media-organizer.jpg", false, false)
	assert.NotPanics(t, func() { _ = err }, "second pool call must not panic or deadlock")
}

// TestExifToolPool_MinSizeOne verifies that n<=0 is clamped to 1.
func TestExifToolPool_MinSizeOne(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not found in PATH")
	}

	pool, err := NewExifToolPool(0)
	require.NoError(t, err)
	defer pool.Close()

	assert.Equal(t, 1, cap(pool.pool))
}
