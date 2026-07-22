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

package boundedfetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry/boundedfetch"
	"github.com/matryer/is"
)

func TestFetch_UnderCapSucceeds(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	data, err := boundedfetch.Fetch(context.Background(), srv.URL, 10)
	is.NoErr(err)
	is.Equal(string(data), "hello")
}

func TestFetch_ExactlyAtCapSucceeds(t *testing.T) {
	is := is.New(t)
	body := strings.Repeat("a", 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	data, err := boundedfetch.Fetch(context.Background(), srv.URL, int64(len(body)))
	is.NoErr(err)
	is.Equal(string(data), body)
}

// TestFetch_OverCapFailsDistinctly proves the +1 byte technique: a body
// exactly one byte over the cap is refused as ErrTooLarge, not silently
// truncated and accepted.
func TestFetch_OverCapFailsDistinctly(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 11)))
	}))
	defer srv.Close()

	_, err := boundedfetch.Fetch(context.Background(), srv.URL, 10)
	is.True(err != nil)
	is.True(cerrors.Is(err, boundedfetch.ErrTooLarge))
}

func TestFetch_NonOKStatus(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := boundedfetch.Fetch(context.Background(), srv.URL, 10)
	is.True(err != nil)
	is.True(!cerrors.Is(err, boundedfetch.ErrTooLarge))
}

func TestFetch_ContextCanceled(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := boundedfetch.Fetch(ctx, srv.URL, 10)
	is.True(err != nil)
}

func TestReadFile_UnderCapSucceeds(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "f.json")
	is.NoErr(os.WriteFile(path, []byte(`{"a":1}`), 0o600))

	data, err := boundedfetch.ReadFile(path, 100)
	is.NoErr(err)
	is.Equal(string(data), `{"a":1}`)
}

func TestReadFile_OverCapFails(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "f.json")
	is.NoErr(os.WriteFile(path, []byte(strings.Repeat("a", 11)), 0o600))

	_, err := boundedfetch.ReadFile(path, 10)
	is.True(err != nil)
	is.True(cerrors.Is(err, boundedfetch.ErrTooLarge))
}

func TestReadFile_MissingFile(t *testing.T) {
	is := is.New(t)
	_, err := boundedfetch.ReadFile(filepath.Join(t.TempDir(), "missing.json"), 10)
	is.True(err != nil)
}
