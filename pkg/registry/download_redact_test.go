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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
)

// An artifact URL can carry credentials as user info. None of Download's
// errors may repeat them.
func TestDownload_ErrorsDoNotContainUserInfo(t *testing.T) {
	withUserInfo := func(u string) string {
		return strings.Replace(u, "http://", "http://SECRET-user:SECRET-pass@", 1)
	}
	dest := func(t *testing.T) string { return filepath.Join(t.TempDir(), "artifact.tar.gz") }

	t.Run("request cannot be built", func(t *testing.T) {
		_, err := registry.Download(context.Background(), nil, "http://SECRET-user:SECRET-pass@host:badport/x", dest(t), 10)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET")
	})
	t.Run("request fails", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := withUserInfo(srv.URL)
		srv.Close()
		_, err := registry.Download(context.Background(), nil, url, dest(t), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not download")
		assert.NotContains(t, err.Error(), "SECRET")
	})
	t.Run("unexpected status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := registry.Download(context.Background(), nil, withUserInfo(srv.URL), dest(t), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 500")
		assert.NotContains(t, err.Error(), "SECRET")
	})
	t.Run("query credentials", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		_, err := registry.Download(context.Background(), nil, srv.URL+"/a.tgz?X-Amz-Signature=SECRET-sig", dest(t), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
		assert.NotContains(t, err.Error(), "SECRET")
	})
}
