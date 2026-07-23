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
	"bytes"
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

// FuzzReadFile exercises the io.LimitReader-plus-one-byte cap check with
// arbitrary content and cap sizes. ReadFile has negligible parse surface (no
// untrusted decoding, just a byte-count comparison), so this fuzz target
// isn't hunting for a parser crash — it pins down the two properties that
// matter: the read never panics, and the ErrTooLarge/success boundary falls
// exactly at maxBytes regardless of what bytes are on either side of it.
func FuzzReadFile(f *testing.F) {
	f.Add([]byte(""), int64(0))
	f.Add([]byte("hello"), int64(10))
	f.Add([]byte(strings.Repeat("a", 10)), int64(10))
	f.Add([]byte(strings.Repeat("a", 11)), int64(10))
	f.Add([]byte{0x00, 0xff, 0x00}, int64(1))

	f.Fuzz(func(t *testing.T, data []byte, maxBytes int64) {
		if maxBytes < 0 || maxBytes > 1<<40 {
			// Out of the range any real caller passes (registry caps are
			// small, fixed constants); avoids int64 overflow in maxBytes+1
			// and giant temp-file writes that would just slow the fuzzer
			// down without exercising new behavior.
			t.Skip("maxBytes outside realistic caller range")
		}

		path := filepath.Join(t.TempDir(), "fuzz-input")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("test setup: writing fixture file: %v", err)
		}

		got, err := boundedfetch.ReadFile(path, maxBytes)
		if int64(len(data)) > maxBytes {
			if !cerrors.Is(err, boundedfetch.ErrTooLarge) {
				t.Fatalf("data len %d > maxBytes %d: want ErrTooLarge, got %v", len(data), maxBytes, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("data len %d <= maxBytes %d: unexpected error: %v", len(data), maxBytes, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("returned data does not match written fixture: got %d bytes, want %d bytes", len(got), len(data))
		}
	})
}
