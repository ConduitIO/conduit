// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registry_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
)

func TestCache_PopulateThenLookup_Hit(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello connector artifact bytes")
	sum := sha256.Sum256(data)
	digestHex := hex.EncodeToString(sum[:])

	require.NoError(t, registry.CachePopulate(dir, digestHex, data, "https://example.test/artifact"))

	got, hit, err := registry.CacheLookup(dir, digestHex)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, data, got)

	meta, err := registry.CacheMetaForTest(dir, digestHex)
	require.NoError(t, err)
	assert.Equal(t, digestHex, meta.SHA256)
	assert.Equal(t, int64(len(data)), meta.Size)
	assert.Equal(t, "https://example.test/artifact", meta.SourceURL)
	assert.False(t, meta.CachedAt.IsZero())
}

func TestCache_Lookup_Miss(t *testing.T) {
	dir := t.TempDir()
	got, hit, err := registry.CacheLookup(dir, "deadbeef")
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Nil(t, got)
}

func TestCache_Populate_DigestMismatch_Refuses(t *testing.T) {
	dir := t.TempDir()
	err := registry.CachePopulate(dir, "0000000000000000000000000000000000000000000000000000000000000000", []byte("x"), "")
	require.Error(t, err)
}

// TestCache_CorruptEntry_DetectedAndEvicted is the AC13 case: a cache entry
// whose stored bytes no longer match the sha256 encoded in its own
// directory name (simulating truncation/bitrot/tampering after the fact)
// must never be served — CacheLookup must report a miss AND remove the
// corrupt entry so a subsequent populate/lookup isn't wedged behind it.
func TestCache_CorruptEntry_DetectedAndEvicted(t *testing.T) {
	dir := t.TempDir()
	data := []byte("original bytes")
	sum := sha256.Sum256(data)
	digestHex := hex.EncodeToString(sum[:])
	require.NoError(t, registry.CachePopulate(dir, digestHex, data, ""))

	// Corrupt it: truncate the stored artifact bytes directly on disk, out
	// from under the cache's own bookkeeping — exactly the "truncate a
	// cached file" scenario the plan calls for.
	artifactPath := filepath.Join(dir, ".registry", "cache", digestHex, "artifact")
	require.NoError(t, os.WriteFile(artifactPath, []byte("corrupted"), 0o600))

	got, hit, err := registry.CacheLookup(dir, digestHex)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Nil(t, got)

	// Evicted: the entry directory must no longer exist at all.
	_, statErr := os.Stat(filepath.Join(dir, ".registry", "cache", digestHex))
	assert.True(t, os.IsNotExist(statErr), "corrupt cache entry should have been evicted, got stat err: %v", statErr)

	// Self-heals: a fresh populate under the same digest succeeds afterward.
	require.NoError(t, registry.CachePopulate(dir, digestHex, data, ""))
	got, hit, err = registry.CacheLookup(dir, digestHex)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, data, got)
}

// TestCache_ConcurrentPopulate_SameDigest_DoesNotCorrupt races N goroutines
// populating the identical content-addressed digest simultaneously — none
// of them may return an error, and the resulting entry must be intact and
// exactly the expected bytes (never a torn/partial write from one racing
// with another's rename).
func TestCache_ConcurrentPopulate_SameDigest_DoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	data := []byte("raced artifact bytes, always identical across goroutines")
	sum := sha256.Sum256(data)
	digestHex := hex.EncodeToString(sum[:])

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = registry.CachePopulate(dir, digestHex, data, "")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}

	got, hit, err := registry.CacheLookup(dir, digestHex)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, data, got)
}

func TestCache_SweepTmp_RemovesOldOrphans(t *testing.T) {
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, ".registry", "cache")
	require.NoError(t, os.MkdirAll(cacheRoot, 0o700))

	oldTmp := filepath.Join(cacheRoot, ".tmp-populate-old")
	require.NoError(t, os.MkdirAll(oldTmp, 0o700))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(oldTmp, oldTime, oldTime))

	freshTmp := filepath.Join(cacheRoot, ".tmp-populate-fresh")
	require.NoError(t, os.MkdirAll(freshTmp, 0o700))

	registry.CacheSweepTmp(dir, 24*time.Hour)

	_, err := os.Stat(oldTmp)
	assert.True(t, os.IsNotExist(err), "old orphaned tmp dir should have been swept")
	_, err = os.Stat(freshTmp)
	assert.NoError(t, err, "fresh tmp dir should NOT have been swept")
}
