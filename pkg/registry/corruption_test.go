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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
)

func TestCheckCorruption_Match(t *testing.T) {
	digest := sha256.Sum256([]byte("hello world"))
	want := hexDigest(digest)
	assert.NoError(t, registry.CheckCorruption(digest, want))
	assert.NoError(t, registry.CheckCorruption(digest, "sha256:"+want))
}

func TestCheckCorruption_Mismatch(t *testing.T) {
	digest := sha256.Sum256([]byte("hello world"))
	other := sha256.Sum256([]byte("goodbye world"))
	err := registry.CheckCorruption(digest, hexDigest(other))
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeCorruptDownload, ce.Code)
}

func TestCheckCorruption_InvalidWant(t *testing.T) {
	digest := sha256.Sum256([]byte("hello world"))
	err := registry.CheckCorruption(digest, "not-hex")
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeCorruptDownload, ce.Code)
}

func hexDigest(d [32]byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return string(out)
}
