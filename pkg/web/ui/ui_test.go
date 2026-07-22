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
	"strings"
	"testing"
	"testing/fstest"

	"github.com/matryer/is"
)

// serve issues method+path against h and returns the recorder, so every test
// below shares one place that builds the request (with a real context, per
// the noctx lint rule) instead of each call site repeating it.
func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	h.ServeHTTP(rec, req)
	return rec
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!doctype html><html><head><title>x</title></head><body>` +
				`<script src="./assets/main.js"></script></body></html>`),
		},
		"assets/main.js": &fstest.MapFile{
			Data: []byte(`console.log("hi")`),
		},
		"assets/dir/nested.css": &fstest.MapFile{
			Data: []byte(`body{}`),
		},
	}
}

func TestFS_EmbeddedBuildIsUsable(t *testing.T) {
	is := is.New(t)

	fsys, err := FS()
	is.NoErr(err)

	h, err := Handler(fsys)
	is.NoErr(err)

	// The real committed dist/ must at least serve a 200 index page — this
	// is the regression guard that dist/ actually got committed (not just an
	// empty/placeholder directory that would fail go:embed at compile time,
	// or a build whose index.html Handler() can't find).
	rec := serve(h, http.MethodGet, "/")
	is.Equal(rec.Code, http.StatusOK)
	is.True(strings.Contains(rec.Header().Get("Content-Type"), "text/html"))
	is.True(strings.Contains(rec.Body.String(), "<base href=\"/\">"))
}

func TestHandler_ServesStaticAssetAsIs(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	rec := serve(h, http.MethodGet, "/assets/main.js")

	is.Equal(rec.Code, http.StatusOK)
	is.Equal(rec.Body.String(), `console.log("hi")`)
	is.True(strings.Contains(rec.Header().Get("Content-Type"), "javascript"))
}

func TestHandler_NestedStaticAssetAsIs(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	rec := serve(h, http.MethodGet, "/assets/dir/nested.css")

	is.Equal(rec.Code, http.StatusOK)
	is.Equal(rec.Body.String(), `body{}`)
}

func TestHandler_UnknownExtensionlessPath_FallsBackToIndex(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	for _, p := range []string{"/pipelines/123", "/fleet", "/anything/nested/route"} {
		rec := serve(h, http.MethodGet, p)

		is.Equal(rec.Code, http.StatusOK)
		is.True(strings.Contains(rec.Header().Get("Content-Type"), "text/html"))
		is.True(strings.Contains(rec.Body.String(), `<script src="./assets/main.js">`))
		// The injected base tag is what makes the relative script src above
		// resolve correctly even though the browser's address bar shows a
		// nested path like /pipelines/123, not /.
		is.True(strings.Contains(rec.Body.String(), `<base href="/">`))
	}
}

func TestHandler_MissingAssetWithExtension_Is404NotIndex(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	rec := serve(h, http.MethodGet, "/assets/does-not-exist.js")

	is.Equal(rec.Code, http.StatusNotFound)
	is.True(!strings.Contains(rec.Body.String(), "<base href"))
}

func TestHandler_DirectoryPath_FallsBackToIndex_NoListing(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	rec := serve(h, http.MethodGet, "/assets")

	// Never a directory listing (which http.FileServer would otherwise
	// produce for a directory path) — falls back to the SPA like any other
	// non-file path.
	is.Equal(rec.Code, http.StatusOK)
	is.True(strings.Contains(rec.Header().Get("Content-Type"), "text/html"))
	is.True(!strings.Contains(rec.Body.String(), "Index of"))
}

func TestHandler_PathTraversal_NeverEscapesBundle(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	for _, p := range []string{
		"/../../../etc/passwd",
		"/assets/../../etc/passwd",
		"/%2e%2e/%2e%2e/etc/passwd",
	} {
		rec := serve(h, http.MethodGet, p)

		// Either a clean 404 or the SPA fallback — never file content from
		// outside the bundle, and never a 5xx from a panicking fs call.
		is.True(rec.Code == http.StatusOK || rec.Code == http.StatusNotFound)
		is.True(!strings.Contains(rec.Body.String(), "root:"))
	}
}

func TestHandler_HeadRequest_NoBody(t *testing.T) {
	is := is.New(t)
	h, err := Handler(testFS())
	is.NoErr(err)

	rec := serve(h, http.MethodHead, "/pipelines/123")

	is.Equal(rec.Code, http.StatusOK)
	is.Equal(rec.Body.Len(), 0)
}

func TestHandler_MissingIndexHTML_Errors(t *testing.T) {
	is := is.New(t)
	fsys := fstest.MapFS{
		"assets/main.js": &fstest.MapFile{Data: []byte("x")},
	}
	_, err := Handler(fsys)
	is.True(err != nil)
}

func TestInjectBaseHref_NoHeadTag_ReturnsUnchanged(t *testing.T) {
	is := is.New(t)
	in := []byte(`<!doctype html><body>no head here</body>`)
	out := injectBaseHref(in)
	is.Equal(string(out), string(in))
}

func TestInjectBaseHref_InsertsRightAfterHead(t *testing.T) {
	is := is.New(t)
	in := []byte(`<html><head><title>x</title></head></html>`)
	out := injectBaseHref(in)
	is.Equal(string(out), `<html><head><base href="/"><title>x</title></head></html>`)
}
