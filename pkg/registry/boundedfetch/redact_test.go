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
	"strings"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/boundedfetch"
)

// A registry URL can carry credentials as user info. None of Fetch's errors
// may repeat them.
func TestFetch_ErrorsDoNotContainUserInfo(t *testing.T) {
	withUserInfo := func(u string) string {
		return strings.Replace(u, "http://", "http://SECRET-user:SECRET-pass@", 1)
	}

	t.Run("request cannot be built", func(t *testing.T) {
		is := is.New(t)
		_, err := boundedfetch.Fetch(context.Background(), "http://SECRET-user:SECRET-pass@host:badport/x", 10)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "could not build request"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
	t.Run("request fails", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		url := withUserInfo(srv.URL)
		srv.Close() // nothing listens any more
		_, err := boundedfetch.Fetch(context.Background(), url, 10)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "could not fetch"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
	t.Run("unexpected status", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := boundedfetch.Fetch(context.Background(), withUserInfo(srv.URL), 10)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "unexpected status 500"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
	t.Run("query credentials", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		_, err := boundedfetch.Fetch(context.Background(), srv.URL+"/bundle?X-Amz-Signature=SECRET-sig&token=SECRET-token", 10)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "unexpected status 403"))
		is.True(strings.Contains(err.Error(), "X-Amz-Signature=xxxxx")) // names stay, values go
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
	t.Run("password with an unencoded slash", func(t *testing.T) {
		is := is.New(t)
		_, err := boundedfetch.Fetch(context.Background(), "http://SECRET-user:SECRET/pass@127.0.0.1:1/x", 10)
		is.True(err != nil)
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
	t.Run("malformed Location header", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://SECRET-user:SECRET-pass@127.0.0.1:badport/x;jsessionid=SECRET-sid")
			w.WriteHeader(http.StatusFound)
		}))
		defer srv.Close()
		_, err := boundedfetch.Fetch(context.Background(), srv.URL+"/bundle", 10)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "Location header"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
}
