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
		{"no scheme and no at sign is left alone", "registry.example.com/index.json", "registry.example.com/index.json"},
		{"empty", "", ""},
		{"matrix parameter", "https://h/p;jsessionid=abc123/q", "https://h/p;jsessionid=xxxxx/q"},
		{"matrix parameter and query", "https://h/p;jsessionid=abc123?a=1", "https://h/p;jsessionid=xxxxx?a=xxxxx"},
		// shapes url.Parse does not read the user info of
		{"password with a slash", "https://user:pa/ss@host/p", "https://redacted:xxxxx@host/p"},
		{"password with a hash", "https://user:pa#ss@host/p", "https://redacted:xxxxx@host/p"},
		{"password with a question mark", "https://user:pa?ss@host/p", "https://redacted:xxxxx@host/p"},
		{"password with all three", "postgres://user:p/a#s?s@host:5432/db", "postgres://redacted:xxxxx@host:5432/db"},
		{"no scheme", "user:pass@host", "redacted:xxxxx@host"},
		{"no scheme with port and path", "user:pass@host:5432/db", "redacted:xxxxx@host:5432/db"},
		{"several at signs", "https://user:p@ss@host/p", "https://redacted:xxxxx@host/p"},
		{"token only, no scheme", "ghp_TOKEN@host/p", "redacted:xxxxx@host/p"},
		{"at sign in a later query", "https://user:pass@host/p?mail=a@b.example", "https://redacted:xxxxx@b.example"},
		{"password with at sign and slash", "https://user:p@ss/word@host/p", "https://redacted:xxxxx@host/p"},
		{"password with at sign and question mark", "https://user:p@ss?word@host/p", "https://redacted:xxxxx@host/p"},
		{"password with at sign and hash", "https://user:p@ss#word@host/p", "https://redacted:xxxxx@host/p"},
		{"at sign in a path, no credentials", "https://host/@scope/pkg", "https://redacted:xxxxx@scope/pkg"}, // over-redacts, on purpose
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
		is.True(strings.Contains(got.Error(), "invalid URL (details not shown)")) // a parse failure's cause quotes the URL
		is.True(!strings.Contains(got.Error(), "invalid port"))
		is.True(cerrors.Is(got, err)) // chain preserved
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
	t.Run("a parse failure never shows its cause", func(t *testing.T) {
		is := is.New(t)
		_, err := url.Parse(badURLNoCreds)
		got := Error(err)
		is.True(strings.Contains(got.Error(), "invalid URL (details not shown)"))
		is.True(!strings.Contains(got.Error(), "invalid port"))
	})
	t.Run("a non-parse url.Error keeps its cause and loses the credentials", func(t *testing.T) {
		is := is.New(t)
		err := &url.Error{Op: "Get", URL: "https://SECRET-user:SECRET-pass@host/p?token=SECRET-token", Err: cerrors.New("connection refused")}
		got := Error(err)
		is.True(strings.Contains(got.Error(), "connection refused"))
		is.True(!strings.Contains(got.Error(), "SECRET"))
	})
}

func TestText(t *testing.T) {
	is := is.New(t)
	got := Text(`unable to GET "http://SECRET-user:SECRET-pass@registry.internal:8081/schemas/ids/3?token=SECRET-token": connection refused; see https://docs.example.com/x`)
	is.True(!strings.Contains(got, "SECRET"))
	is.True(strings.Contains(got, "registry.internal:8081/schemas/ids/3?token=xxxxx"))
	is.True(strings.Contains(got, "connection refused"))
	is.True(strings.Contains(got, "https://docs.example.com/x"))
}

func TestError_RedactsURLsTheLibraryFormattedItself(t *testing.T) {
	is := is.New(t)
	err := cerrors.Errorf(`unable to GET "http://SECRET-user:SECRET-pass@registry.internal/subjects": %w`, cerrors.New("connection refused"))
	got := Error(err)
	is.True(!strings.Contains(got.Error(), "SECRET"))
	is.True(cerrors.Is(got, err))
}

func TestText_Shapes(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"comma joined urls": {
			"tried https://u1:p1@h1/x,https://u2:p2@h2/y failed",
			"tried https://redacted:xxxxx@h1/x,https://redacted:xxxxx@h2/y failed",
		},
		"password with a single quote":    {`GET "http://u:pa'ss@h/p": refused`, `GET "http://redacted:xxxxx@h/p": refused`},
		"password with a double quote":    {`GET http://u:pa"ss@h/p refused`, `GET http://redacted:xxxxx@h/p refused`},
		"password with a space":           {`GET http://u:pa ss@h/p refused`, `GET http://redacted:xxxxx@h/p refused`},
		"password with an angle bracket":  {`GET http://u:pa<ss@h/p refused`, `GET http://redacted:xxxxx@h/p refused`},
		"query masked":                    {`GET http://h/p?token=abc refused`, `GET http://h/p?token=xxxxx refused`},
		"scheme inside a password":        {`GET http://u:SECRET://x@h/p refused`, `GET http://redacted:xxxxx@h/p refused`},
		"scheme inside a second password": {`tried https://u1:aaa://b@h1/x,https://u2:ccc://d@h2/y failed`, `tried https://redacted:xxxxx@h1/x,https://redacted:xxxxx@h2/y failed`},
		"matrix parameter":                {`GET http://h/p;jsessionid=abc refused`, `GET http://h/p;jsessionid=xxxxx refused`},
		"url after an equals sign":        {`url=http://u:p@h/x`, `url=http://redacted:xxxxx@h/x`},
		"no url":                          {"connection refused", "connection refused"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			got := Text(tc.in)
			is.Equal(got, tc.want)
			is.True(!strings.Contains(got, "p1") && !strings.Contains(got, "p2") && !strings.Contains(got, "ss@"))
		})
	}
}

func TestError_PasswordWithAtSignAndDelimiter(t *testing.T) {
	is := is.New(t)
	err := &url.Error{Op: "Get", URL: "https://user:p@ss/word@host/p", Err: cerrors.New("connection refused")}
	got := Error(err)
	is.True(strings.Contains(got.Error(), "connection refused"))
	is.True(!strings.Contains(got.Error(), "ss/word"))
	is.True(strings.Contains(got.Error(), "redacted:xxxxx@host/p"))
}

// The HTTP client puts a malformed Location header, which can hold credentials,
// into the cause of its error: the cause text goes through Text as well.
func TestError_MalformedLocationHeaderCause(t *testing.T) {
	is := is.New(t)
	err := &url.Error{Op: "Get", URL: "https://registry.example.com/a", Err: cerrors.New(`failed to parse Location header "http://user:SECRET-pass@other.example.com/x;jsessionid=SECRET-sid": invalid port`)}
	got := Error(err)
	is.True(!strings.Contains(got.Error(), "SECRET"))
	is.True(strings.Contains(got.Error(), "failed to parse Location header"))
}
