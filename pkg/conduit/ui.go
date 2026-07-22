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
	"net/http"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/log"
	webui "github.com/conduitio/conduit/pkg/web/ui"
)

// reservedAPIPaths are the routes serveHTTPAPI already registers on gwmux
// (see runtime.go): /v1/* (the gRPC-gateway-forwarded API, including the
// websocket-proxied Inspect streams), /openapi/* (the swagger UI),
// /healthz, /readyz, and /metrics. uiMiddleware never serves the UI for a
// request under one of these — see docs/design-documents/
// 20260713-greenfield-built-in-ui.md §7 and the route-collision test in
// ui_test.go.
// Path constants for the engine's existing routes (see runtime.go), shared
// with ui_test.go's route-collision assertions so the two never drift apart.
const (
	pathV1      = "/v1"
	pathOpenAPI = "/openapi"
	pathHealthz = "/healthz"
	pathReadyz  = "/readyz"
	pathMetrics = "/metrics"
)

var reservedAPIPaths = []string{
	pathV1,
	pathOpenAPI,
	pathHealthz,
	pathReadyz,
	pathMetrics,
}

// isReservedAPIPath reports whether p is, or is nested under, one of
// reservedAPIPaths (exact match or a "/"-bounded prefix, so "/v1" matches
// but "/v1foo" does not).
func isReservedAPIPath(p string) bool {
	for _, prefix := range reservedAPIPaths {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// uiMiddleware wraps next — the fully assembled gateway/CORS/websocket
// handler chain buildAPIHandler returns — so that GET/HEAD requests for
// anything NOT under reservedAPIPaths are served by the embedded built-in
// UI (pkg/web/ui) instead, when enabled.
//
// This is the disable mechanism from the design doc §7 (the swagger-ui
// precedent: the assets stay embedded in the binary either way — see
// pkg/web/ui's doc comment — only the route registration is gated). When
// enabled is false, next is returned completely unchanged: "/" was never
// claimed by next, so a request to it keeps whatever next already does with
// an unregistered path (grpc-gateway's own JSON 404), and every other
// existing route is provably untouched because this function didn't touch
// them either.
//
// Mux ordering (the route-collision requirement from AC #7): the
// reserved-path check runs before the UI ever sees the request, so
// /v1/*, /openapi/*, /healthz, /readyz, /metrics always reach next first
// and can never be shadowed by the SPA catch-all — the same guarantee
// "mount the SPA last" gives, expressed as an explicit check instead of
// registration order (grpc-gateway's ServeMux dispatches by first
// registration-order pattern match per method, which would make ordering
// correct-but-implicit; this makes it correct-and-explicit, and testable
// without needing a live gRPC backend to register the real API handlers).
func uiMiddleware(ctx context.Context, next http.Handler, enabled bool, logger log.CtxLogger) http.Handler {
	if !enabled {
		return next
	}

	uiFS, err := webui.FS()
	if err != nil {
		logger.Warn(ctx).Err(err).Msg("embedded UI assets unusable, serving API only (UI route disabled for this run)")
		return next
	}
	uiHandler, err := webui.Handler(uiFS)
	if err != nil {
		logger.Warn(ctx).Err(err).Msg("failed to construct embedded UI handler, serving API only (UI route disabled for this run)")
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isReservedAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// The UI has no non-GET/HEAD routes. Let the API handler answer
			// (its normal 404/405) instead of serving HTML for e.g. a stray
			// POST to "/".
			next.ServeHTTP(w, r)
			return
		}
		uiHandler.ServeHTTP(w, r)
	})
}
