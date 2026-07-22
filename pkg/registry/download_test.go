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
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
)

func TestDownload_Succeeds(t *testing.T) {
	body := []byte("connector-archive-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "artifact.tar.gz")
	res, err := registry.Download(context.Background(), nil, srv.URL, dest, int64(len(body)))
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), res.Size)
	assert.Equal(t, sha256.Sum256(body), res.Digest)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestDownload_FollowsRedirect(t *testing.T) {
	body := []byte("redirected-bytes")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer target.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer gateway.Close()

	dest := filepath.Join(t.TempDir(), "artifact.tar.gz")
	res, err := registry.Download(context.Background(), nil, gateway.URL, dest, int64(len(body)))
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), res.Size)
}

func TestDownload_RedirectLoopFails(t *testing.T) {
	var loopURL string
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loopURL, http.StatusFound)
	}))
	defer loop.Close()
	loopURL = loop.URL

	dest := filepath.Join(t.TempDir(), "artifact.tar.gz")
	_, err := registry.Download(context.Background(), nil, loop.URL, dest, 1024)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeDownloadFailed, ce.Code)
}

func TestDownload_ExceedsDeclaredSizeFails(t *testing.T) {
	body := make([]byte, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "artifact.tar.gz")
	_, err := registry.Download(context.Background(), nil, srv.URL, dest, 10) // declared size far below actual
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeDownloadFailed, ce.Code)
}

func TestDownload_NonSuccessStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "artifact.tar.gz")
	_, err := registry.Download(context.Background(), nil, srv.URL, dest, 1024)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeDownloadFailed, ce.Code)
}
