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

package conduit

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	webui "github.com/conduitio/conduit/pkg/web/ui"
	"github.com/matryer/is"
)

func TestIsReservedAPIPath(t *testing.T) {
	testCases := []struct {
		path string
		want bool
	}{
		{pathV1, true},
		{pathV1 + "/pipelines", true},
		{pathV1 + "/connectors/x/inspect", true},
		{"/v1foo", false}, // must be "/"-bounded, not a bare prefix match
		{pathOpenAPI, true},
		{pathOpenAPI + "/", true},
		{pathOpenAPI + "/api/v1/api.swagger.json", true},
		{pathHealthz, true},
		{pathReadyz, true},
		{pathMetrics, true},
		{"/", false},
		{"/pipelines/123", false},
		{"/fleet", false},
		{"/assets/main.js", false},
	}
	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			is := is.New(t)
			is.Equal(isReservedAPIPath(tc.path), tc.want)
		})
	}
}

// stubAPIHandler mimics the real gwmux + existing-route behavior closely
// enough to prove uiMiddleware's wiring: each reserved path returns the
// distinct status/content-type/body the real handler would, and an
// unmatched /v1/* path returns grpc-gateway's own JSON 404 (never HTML) —
// the specific regression this test guards against.
func stubAPIHandler(reached *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reached != nil {
			*reached = append(*reached, r.URL.Path)
		}
		switch {
		case r.URL.Path == pathV1+"/pipelines":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"pipelines":[]}`))
		case strings.HasPrefix(r.URL.Path, pathV1+"/"):
			// grpc-gateway's routingErrorHandler: an unmatched /v1/* path is
			// a JSON 404, not HTML — this must survive the SPA being mounted.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found"}`))
		case strings.HasPrefix(r.URL.Path, pathOpenAPI):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html>swagger</html>`))
		case r.URL.Path == pathHealthz:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"SERVING"}`))
		case r.URL.Path == pathReadyz:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		case r.URL.Path == pathMetrics:
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# HELP conduit_up 1\n"))
		default:
			// "/" and everything else grpc-gateway has no route for.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found"}`))
		}
	})
}

type recordedResponse struct {
	status      int
	contentType string
	body        string
}

func doGet(t *testing.T, h http.Handler, method, path string) recordedResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	h.ServeHTTP(rec, req)
	return recordedResponse{
		status:      rec.Code,
		contentType: rec.Header().Get("Content-Type"),
		body:        rec.Body.String(),
	}
}

// TestUIMiddleware_RouteCollision proves the AC #7 requirement directly: with
// the embedded UI mounted, every existing route's response (status,
// content-type, body) is byte-identical to what it would be without the UI —
// including an unmatched /v1/* path staying a JSON 404 instead of being
// swallowed into the SPA's HTML fallback.
func TestUIMiddleware_RouteCollision(t *testing.T) {
	is := is.New(t)
	inner := stubAPIHandler(nil)
	wrapped := uiMiddleware(context.Background(), inner, true, log.Nop())

	existingPaths := []string{
		pathV1 + "/pipelines",
		pathV1 + "/does-not-exist", // unmatched API path: must stay JSON, never HTML
		pathOpenAPI + "/",
		pathOpenAPI + "/api/v1/api.swagger.json",
		pathHealthz,
		pathReadyz,
		pathMetrics,
	}
	for _, p := range existingPaths {
		t.Run(p, func(t *testing.T) {
			want := doGet(t, inner, http.MethodGet, p)
			got := doGet(t, wrapped, http.MethodGet, p)
			is.Equal(got.status, want.status)
			is.Equal(got.contentType, want.contentType)
			is.Equal(got.body, want.body)
		})
	}

	// The regression this whole test exists to catch: an unmatched /v1/*
	// path must never come back as HTML.
	got := doGet(t, wrapped, http.MethodGet, pathV1+"/does-not-exist")
	is.True(strings.Contains(got.contentType, "json"))
	is.True(!strings.Contains(got.body, "<html"))
}

// TestUIMiddleware_UnknownPath_ServesSPA proves the other half of AC #7: a
// client-side route not claimed by any existing API path is served by the
// embedded UI (200, text/html) and never reaches the API handler at all.
func TestUIMiddleware_UnknownPath_ServesSPA(t *testing.T) {
	is := is.New(t)
	var reached []string
	inner := stubAPIHandler(&reached)
	wrapped := uiMiddleware(context.Background(), inner, true, log.Nop())

	got := doGet(t, wrapped, http.MethodGet, "/pipelines/123")
	is.Equal(got.status, http.StatusOK)
	is.True(strings.Contains(got.contentType, "text/html"))
	is.True(len(reached) == 0) // never reached the API handler
}

// TestUIMiddleware_NonGET_FallsThroughToAPI proves the UI only ever answers
// GET/HEAD — anything else (e.g. a stray POST to "/") reaches the API
// handler and gets its normal response, not HTML.
func TestUIMiddleware_NonGET_FallsThroughToAPI(t *testing.T) {
	is := is.New(t)
	var reached []string
	inner := stubAPIHandler(&reached)
	wrapped := uiMiddleware(context.Background(), inner, true, log.Nop())

	got := doGet(t, wrapped, http.MethodPost, "/")
	is.Equal(got.status, http.StatusNotFound) // stub's default for "/"
	is.True(strings.Contains(got.contentType, "json"))
	is.Equal(reached, []string{"/"})
}

// TestUIMiddleware_Disabled_LeavesEverythingToTheAPIHandler is the disable
// mechanism's contract: when disabled, uiMiddleware must return next
// unchanged — not a wrapper that happens to behave the same, the same
// handler — so there is no code path left that could serve the SPA, and
// every route (including the previously-unclaimed "/") keeps exactly
// whatever behavior next already had.
func TestUIMiddleware_Disabled_LeavesEverythingToTheAPIHandler(t *testing.T) {
	is := is.New(t)
	inner := stubAPIHandler(nil)

	got := uiMiddleware(context.Background(), inner, false, log.Nop())
	is.Equal(got, inner) // literally the same handler, not a passthrough wrapper

	for _, p := range []string{"/", "/pipelines/123", pathV1 + "/pipelines", pathHealthz} {
		want := doGet(t, inner, http.MethodGet, p)
		gotResp := doGet(t, got, http.MethodGet, p)
		is.Equal(gotResp, want)
	}
}

// TestUIMiddleware_Enabled_StaticAsset proves a real embedded static asset
// (discovered at test time so this isn't pinned to a build-specific hashed
// filename) is served as-is through the middleware.
func TestUIMiddleware_Enabled_StaticAsset(t *testing.T) {
	is := is.New(t)
	uiFS, err := webui.FS()
	is.NoErr(err)

	var assetPath string
	err = fs.WalkDir(uiFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || assetPath != "" || d.IsDir() {
			return err
		}
		assetPath = p
		return nil
	})
	is.NoErr(err)
	is.True(assetPath != "") // dist/assets must be non-empty for this test to mean anything

	inner := stubAPIHandler(nil)
	wrapped := uiMiddleware(context.Background(), inner, true, log.Nop())

	got := doGet(t, wrapped, http.MethodGet, "/"+assetPath)
	is.Equal(got.status, http.StatusOK)
	is.True(got.body != "")
}
