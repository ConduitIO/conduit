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

import "net/http"

// ContentSecurityPolicy is sent on every response of the HTTP API, which serves
// the built-in UI, the Swagger UI and JSON.
//
//   - frame-ancestors 'none' (with X-Frame-Options: DENY for old browsers):
//     the UI cannot be framed, so it cannot be clickjacked.
//   - script-src 'self': only scripts served by Conduit run; no inline script
//     and no eval. The UI bundle and conduit-auth.js are same-origin files.
//   - style-src allows 'unsafe-inline' because the UI bundle and Swagger UI inject
//     <style> elements. Script execution is what the policy protects.
//   - connect-src 'self': fetch and WebSocket go to the page's own origin only
//     (CSP3 matches ws/wss of the same host with 'self').
//   - object-src 'none', base-uri 'self', form-action 'self'.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// SecurityHeaders sets the framing, content-type and referrer headers on every
// response, including refusals.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
