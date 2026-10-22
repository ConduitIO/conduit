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

package apisec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

const goodToken = "0123456789abcdef0123456789abcdef"

func TestResolveToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	testCases := []struct {
		name      string
		token     string
		file      string
		want      string
		wantCode  *conduiterr.Code
		wantPath  string
		leakCheck string // must not appear in the error text
	}{
		{name: "none configured"},
		{name: "inline", token: " " + goodToken + " ", want: goodToken},
		{name: "file is trimmed", file: write("ok", "\n"+goodToken+"\n"), want: goodToken},
		{name: "short inline", token: "abc123xyz", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abc123xyz"},
		{name: "short file", file: write("short", "tiny\n"), wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "tiny"},
		{name: "empty file", file: write("empty", " \n"), wantCode: &CodeTokenInvalid, wantPath: ConfigPathTokenFile},
		{name: "missing file", file: filepath.Join(dir, "missing"), wantCode: &CodeTokenInvalid, wantPath: ConfigPathTokenFile},
		{name: "whitespace-only inline token is not 'unset'", token: "                  ", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken},
		{name: "tab-only inline token", token: "\t\t", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken},
		{name: "space inside inline", token: "0123456789 abcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "tab inside inline", token: "0123456789\tabcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "newline inside inline", token: "0123456789\nabcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "NUL inside inline", token: "0123456789\x00abcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "DEL inside inline", token: "0123456789\x7fabcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "no-break space inside inline", token: "0123456789\u00a0abcdefghij", wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "space inside file token", file: write("inner", "0123456789 abcdefghij\n"), wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "abcdefghij"},
		{name: "two lines in a file", file: write("twolines", "0123456789abcdef\nsecondline-abcdef\n"), wantCode: &CodeTokenInvalid, wantPath: ConfigPathToken, leakCheck: "secondline"},
		{name: "both set", token: goodToken, file: write("both", goodToken), wantCode: &CodeTokenInvalid, wantPath: ConfigPathTokenFile},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got, err := ResolveToken(tc.token, tc.file)
			if tc.wantCode == nil {
				is.NoErr(err)
				is.Equal(got, tc.want)
				return
			}
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, *tc.wantCode)
			is.Equal(ce.ConfigPath, tc.wantPath)
			is.True(ce.Suggestion != "")
			if tc.leakCheck != "" {
				is.True(!strings.Contains(ce.Message, tc.leakCheck))
			}
		})
	}
}

func TestBearerToken_SchemeIsCaseInsensitive(t *testing.T) {
	is := is.New(t)
	for _, in := range []string{"Bearer abc", "bearer abc", "BEARER abc"} {
		got, ok := bearerToken(in)
		is.True(ok)
		is.Equal(got, "abc")
	}
	for _, in := range []string{"", "abc", "Basic abc", "Bearerabc", "Bear"} {
		_, ok := bearerToken(in)
		is.True(!ok)
	}
}

func TestTokensEqual(t *testing.T) {
	is := is.New(t)
	is.True(tokensEqual(goodToken, goodToken))
	is.True(!tokensEqual(goodToken, goodToken+"x"))
	is.True(!tokensEqual(goodToken, goodToken[:len(goodToken)-1]))
	is.True(!tokensEqual(goodToken, ""))
	is.True(!tokensEqual("", goodToken))
	is.True(!tokensEqual(goodToken, "X"+goodToken[1:]))
}

// TestTokensEqual_TimingDoesNotDependOnMatchLength is a coarse guard against
// replacing the comparison with an early-exit one (== or bytes.Equal). It
// compares the time to reject a token that differs in its first byte with one
// that differs in its last. An early-exit comparison of 64 bytes is not
// reliably separable from noise at this size, so this is a tripwire, not a
// proof; the guarantee is by construction (fixed-size digests compared with
// crypto/subtle).
func TestTokensEqual_TimingDoesNotDependOnMatchLength(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	secret := strings.Repeat("a", 1024)
	early := "b" + secret[1:]
	late := secret[:len(secret)-1] + "b"

	measure := func(candidate string) time.Duration {
		best := time.Duration(1<<63 - 1)
		for run := 0; run < 7; run++ {
			start := time.Now()
			for i := 0; i < 500; i++ {
				tokensEqual(secret, candidate)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	e, l := measure(early), measure(late)
	ratio := float64(e) / float64(l)
	if ratio < 0.5 || ratio > 2 {
		t.Fatalf("rejecting an early mismatch took %v, a late mismatch %v (ratio %.2f): comparison time depends on where the mismatch is", e, l, ratio)
	}
}

func okHandler() (http.Handler, *int) {
	n := 0
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { n++; w.WriteHeader(http.StatusOK) }), &n
}

func TestAuthenticator_HTTP(t *testing.T) {
	testCases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		{"no header", "GET", "/v1/pipelines", nil, 401},
		{"wrong token", "GET", "/v1/pipelines", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"empty bearer", "GET", "/v1/pipelines", map[string]string{"Authorization": "Bearer "}, 401},
		{"right token", "GET", "/v1/pipelines", map[string]string{"Authorization": "Bearer " + goodToken}, 200},
		{"lowercase scheme", "GET", "/v1/pipelines", map[string]string{"Authorization": "bearer " + goodToken}, 200},
		{"token in query is ignored", "GET", "/v1/pipelines?access_token=" + goodToken, nil, 401},
		{"token in cookie is ignored", "GET", "/v1/pipelines", map[string]string{"Cookie": "token=" + goodToken}, 401},
		{"metrics gated", "GET", "/metrics", nil, 401},
		{"healthz open", "GET", "/healthz", nil, 200},
		{"readyz open", "GET", "/readyz", nil, 200},
		{"healthz POST gated", "POST", "/healthz", nil, 401},
		{"healthz with suffix gated", "GET", "/healthz/x", nil, 401},
		{"preflight open", "OPTIONS", "/v1/pipelines", map[string]string{"Access-Control-Request-Method": "POST"}, 200},
		{"plain OPTIONS gated", "OPTIONS", "/v1/pipelines", nil, 401},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			next, calls := okHandler()
			h := NewAuthenticator(goodToken, NewRejections(nil, log.Nop())).HTTP(next)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			is.Equal(rec.Code, tc.want)
			if tc.want == 401 {
				is.Equal(*calls, 0) // never reaches the handler
				is.Equal(rec.Header().Get("WWW-Authenticate"), `Bearer realm="conduit"`)
				is.True(strings.Contains(rec.Body.String(), "api.unauthenticated"))
				is.True(strings.Contains(rec.Body.String(), "configPath"))
			}
		})
	}
}

func TestAuthenticator_DoesNotLogCredentials(t *testing.T) {
	is := is.New(t)
	var sb strings.Builder
	logger := log.New(zerologNew(&sb))
	next, _ := okHandler()
	h := NewAuthenticator(goodToken, NewRejections(nil, logger)).HTTP(next)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/pipelines", nil)
	req.Header.Set("Authorization", "Bearer presented-secret-value")
	h.ServeHTTP(httptest.NewRecorder(), req)
	is.True(sb.Len() > 0)
	is.True(!strings.Contains(sb.String(), "presented-secret-value"))
	is.True(!strings.Contains(sb.String(), goodToken))
}
