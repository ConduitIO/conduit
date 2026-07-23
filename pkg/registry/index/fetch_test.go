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

package index_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

func TestFetch_Success(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"payload":{},"signatures":[]}`))
	}))
	defer srv.Close()

	data, err := index.Fetch(context.Background(), srv.URL)
	is.NoErr(err)
	is.Equal(string(data), `{"payload":{},"signatures":[]}`)
}

func TestFetch_TooLarge(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", int(index.MaxIndexBytes)+1)))
	}))
	defer srv.Close()

	_, err := index.Fetch(context.Background(), srv.URL)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexTooLarge)
}

func TestFetch_Unreachable(t *testing.T) {
	is := is.New(t)
	_, err := index.Fetch(context.Background(), "http://127.0.0.1:0/does-not-exist")
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexUnreachable)
}

func TestFetchFile_Success(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "index.json")
	is.NoErr(os.WriteFile(path, []byte(`{"payload":{},"signatures":[]}`), 0o600))

	data, err := index.FetchFile(path)
	is.NoErr(err)
	is.Equal(string(data), `{"payload":{},"signatures":[]}`)
}

func TestFetchFile_TooLarge(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "index.json")
	is.NoErr(os.WriteFile(path, []byte(strings.Repeat("a", int(index.MaxIndexBytes)+1)), 0o600))

	_, err := index.FetchFile(path)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexTooLarge)
}

func TestFetchFile_Missing(t *testing.T) {
	is := is.New(t)
	_, err := index.FetchFile(filepath.Join(t.TempDir(), "missing.json"))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, index.CodeIndexUnreachable)
}
