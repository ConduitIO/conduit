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

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/matryer/is"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	fsys, err := FS()
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))
	return rec
}

func TestAuthScript_IsServedAndInjectedBeforeTheBundle(t *testing.T) {
	is := is.New(t)
	h := newHandler(t)

	rec := get(t, h, http.MethodGet, "/conduit-auth.js")
	is.Equal(rec.Code, 200)
	is.True(strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript"))
	is.Equal(rec.Header().Get("Cache-Control"), "no-cache")
	is.Equal(rec.Body.String(), string(authScript))

	rec = get(t, h, http.MethodHead, "/conduit-auth.js")
	is.Equal(rec.Code, 200)
	is.Equal(rec.Body.Len(), 0)

	// Every entry point (root, client routes, index.html) loads the script, as
	// a blocking classic script ahead of the module bundle.
	for _, p := range []string{"/", "/index.html", "/pipelines/123"} {
		body := get(t, h, http.MethodGet, p).Body.String()
		tag := `<script src="/conduit-auth.js"></script>`
		i, j := strings.Index(body, tag), strings.Index(body, `type="module"`)
		is.True(i >= 0)
		is.True(j >= 0)
		is.True(i < j)
		is.True(strings.Index(body, `<base href="/">`) < i) // absolute URL, but keep the order stable
	}
}

// The token must live in sessionStorage only and travel only in the
// Authorization header or the WebSocket subprotocol. These are text guards on
// the shipped script; the behavior is tested in conduit-auth.test.mjs.
func TestAuthScript_NeverUsesPersistentStoresOrURLs(t *testing.T) {
	src := string(authScript)
	// Strip comments so the explanation of what is forbidden does not trip the guard.
	var code strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "//") {
			continue
		}
		code.WriteString(line + "\n")
	}
	for _, forbidden := range []string{"localStorage", "document.cookie", "indexedDB", "access_token", "?token", "&token", "location.search", "history.pushState", "history.replaceState", "console."} {
		if strings.Contains(code.String(), forbidden) {
			t.Errorf("conduit-auth.js must not contain %q", forbidden)
		}
	}
	if !strings.Contains(src, "sessionStorage") {
		t.Error("conduit-auth.js must keep the token in sessionStorage")
	}
}

func TestAuthScript_Behavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the script's behavior tests (conduit-auth.test.mjs) were not run")
	}
	out, err := exec.CommandContext(context.Background(), node, "--test", "conduit-auth.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("conduit-auth.test.mjs failed:\n%s", out)
	}
}
