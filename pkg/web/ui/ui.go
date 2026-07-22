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

// Package ui embeds and serves conduit-ui, Conduit's built-in web UI
// (observe + operate). See
// docs/design-documents/20260713-greenfield-built-in-ui.md §7 for the
// embedding/routing contract this package implements, and UI-7's PR
// description for the asset-pipeline decision.
//
// dist/ is a build-time asset, not built from source in this repo: it is the
// output of `npm ci && npm run build` in a checkout of the separate
// https://github.com/ConduitIO/conduit-ui repository, committed here and
// re-embedded deliberately via `make ui` (see the Makefile target and its
// comment for the pinned commit). Source maps are stripped before commit —
// they are a local-dev artifact, not something a production binary should
// ship (they roughly quintuple the embedded size for zero runtime benefit).
//
// Invariant: this package never serves anything outside dist/. embed.FS
// (accessed only through fs.Sub, which enforces fs.ValidPath) makes ".."
// path-traversal structurally impossible — there is no OS filesystem call in
// this package at all.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

//go:embed dist
var distFS embed.FS

// indexPath is the SPA entry point, relative to the root FS returned by FS().
const indexPath = "index.html"

// FS returns the embedded SPA build rooted at dist/, so paths within it are
// relative (e.g. "index.html", "assets/main.js") the way Handler expects.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable in practice: "dist" is a literal go:embed root fixed at
		// compile time, not user input.
		return nil, cerrors.Errorf("ui: embedded dist is not usable: %w", err)
	}
	return sub, nil
}

// Handler serves the embedded SPA build rooted at fsys (normally FS()):
//
//   - A path that names a real file in the bundle (e.g. /assets/main.js) is
//     served as-is with its normal content type.
//   - A path with a file extension that is NOT in the bundle is a genuine 404
//     — a broken or stale asset reference must not be masked as a valid page.
//   - Every other path (no file extension — a client-side React Router
//     route, e.g. /pipelines/123) falls back to index.html with 200 OK so
//     the SPA can take over and resolve it. This must be 200, not a
//     redirect: a hard reload or a shared deep link has to render on the
//     first response.
//   - Directory paths (e.g. /assets/) never get a directory listing — they
//     fall back to index.html like any other non-file path, so this handler
//     can never leak the bundle's file layout.
//
// index.html is served with an injected <base href="/"> so its relative
// asset URLs (conduit-ui's vite.config.ts intentionally builds with a
// relative base, so the same dist/ also works when opened with no server at
// all, e.g. `vite preview`) resolve against the site root even when the
// browser's address bar shows a nested client-route path — without the
// injected base, GET /pipelines/123 would return index.html whose
// script/link tags resolve relative to /pipelines/, a 404. The engine always
// mounts this handler at "/", so patching server-side here is simpler and
// safer than changing the upstream build's base for one embedder.
func Handler(fsys fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(fsys, indexPath)
	if err != nil {
		return nil, cerrors.Errorf("ui: embedded %s not found: %w", indexPath, err)
	}
	index = injectBaseHref(index)

	fileServer := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if rel == "" || rel == "." || rel == indexPath {
			serveIndex(w, r, index)
			return
		}

		if fi, statErr := fs.Stat(fsys, rel); statErr == nil && !fi.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		if strings.Contains(path.Base(rel), ".") {
			// Looks like a static asset request (has a file extension) that
			// isn't in the bundle: a real 404, not a client-side route.
			http.NotFound(w, r)
			return
		}

		// No extension and not a known file or directory: a client-side
		// route. Serve index.html so the SPA can resolve it.
		serveIndex(w, r, index)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(index)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(index)
}

// injectBaseHref inserts <base href="/"> immediately after the first <head>
// tag so the SPA's relative asset URLs resolve against site root regardless
// of the request path that triggered serving index.html. If <head> isn't
// found (an unexpectedly reshaped index.html), it returns html unchanged —
// degrading to "assets break on nested paths" rather than panicking.
func injectBaseHref(html []byte) []byte {
	const (
		marker = "<head>"
		base   = `<base href="/">`
	)
	idx := bytes.Index(html, []byte(marker))
	if idx == -1 {
		return html
	}
	insertAt := idx + len(marker)
	out := make([]byte, 0, len(html)+len(base))
	out = append(out, html[:insertAt]...)
	out = append(out, base...)
	out = append(out, html[insertAt:]...)
	return out
}
