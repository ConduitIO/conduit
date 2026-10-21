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

package urlredact

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// badURL is rejected by url.Parse (invalid port) and carries credentials.
var (
	badURL        = "https://SECRET-user:SECRET-pass@host:" + "badport/x"
	badURLNoCreds = "https://host:" + "badport/x"
)

func TestString(t *testing.T) {
	testCases := []struct{ name, in, want string }{
		{"no user info or query", "https://registry.example.com/index.json", "https://registry.example.com/index.json"},
		{"query values are masked", "https://registry.example.com/i.json?token=abc&X-Amz-Signature=def&flag", "https://registry.example.com/i.json?token=xxxxx&X-Amz-Signature=xxxxx&xxxxx"},
		{"semicolon separated query", "https://h/p?a=1;b=2", "https://h/p?a=xxxxx;b=xxxxx"},
		{"empty query", "https://h/p?", "https://h/p?"},
		{"fragment is masked", "https://h/p#access_token=abc", "https://h/p#xxxxx"},
		{"query and fragment", "https://h/p?k=v#frag", "https://h/p?k=xxxxx#xxxxx"},
		{"user info and query", "https://u:p@h/p?sig=abc", "https://redacted:xxxxx@h/p?sig=xxxxx"},
		{"unparseable with query", "https://u:p@h:bad/p?sig=abc", "https://redacted:xxxxx@h:bad/p?sig=xxxxx"},
		{"user and password", "https://user:pass@registry.example.com/i.json", "https://redacted:xxxxx@registry.example.com/i.json"},
		{"token as user name", "https://ghp_TOKEN@registry.example.com", "https://redacted:xxxxx@registry.example.com"},
		{"password only", "https://:pass@registry.example.com", "https://redacted:xxxxx@registry.example.com"},
		{"unparseable port", "https://user:pass@registry.example.com:bad/x", "https://redacted:xxxxx@registry.example.com:bad/x"},
		{"percent escape in password", "https://user:p%zzss@registry.example.com", "https://redacted:xxxxx@registry.example.com"},
		{"no scheme is left alone", "registry.example.com/index.json", "registry.example.com/index.json"},
		{"empty", "", ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(String(tc.in), tc.want)
		})
	}
}

func TestError(t *testing.T) {
	t.Run("url.Error from Parse", func(t *testing.T) {
		is := is.New(t)
		_, err := url.Parse(badURL)
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "SECRET")) // the premise

		got := Error(err)
		is.True(!strings.Contains(got.Error(), "SECRET"))
		is.True(strings.Contains(got.Error(), "invalid port")) // the cause stays
		is.True(cerrors.Is(got, err))                          // chain preserved
	})
	t.Run("wrapped url.Error", func(t *testing.T) {
		is := is.New(t)
		_, perr := url.Parse(badURL)
		err := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", perr))

		got := Error(err)
		is.True(!strings.Contains(got.Error(), "SECRET"))
		is.True(strings.HasPrefix(got.Error(), "outer: middle: "))
		var ue *url.Error
		is.True(cerrors.As(got, &ue))
	})
	t.Run("no url.Error is returned as is", func(t *testing.T) {
		is := is.New(t)
		err := cerrors.New("boom")
		is.Equal(Error(err), err)
		is.Equal(Error(nil), nil)
	})
	t.Run("url.Error without credentials is returned as is", func(t *testing.T) {
		is := is.New(t)
		_, err := url.Parse(badURLNoCreds)
		is.Equal(Error(err), err)
	})
}
