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
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// BrowserPolicy decides which browser-originated requests may reach the HTTP
// API. See Guard.
type BrowserPolicy struct {
	// AllowedOrigins is the exact-match CORS allowlist ("*" allows any
	// origin). Same-origin requests are always allowed.
	AllowedOrigins []string
	// ValidateHost turns on Host header validation. Set it for loopback binds,
	// where a Host that is not a loopback name can only come from DNS
	// rebinding. On a network bind the legitimate names are unknowable, and
	// the bearer token is what protects the API instead.
	ValidateHost bool
	// AllowedHosts are extra host names (no port) accepted when ValidateHost
	// is set, on top of localhost, 127.0.0.1 and [::1].
	AllowedHosts []string

	// Rejections counts and logs refusals; nil disables both.
	Rejections *Rejections
}

// hostName returns the lower-cased host part of an HTTP Host value, without
// port or IPv6 brackets.
func hostName(hostport string) string {
	h := hostport
	if sh, _, err := net.SplitHostPort(hostport); err == nil {
		h = sh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(h)
}

// HostAllowed reports whether host (a Host header value) is acceptable. It is
// always true when ValidateHost is off.
func (p BrowserPolicy) HostAllowed(host string) bool {
	if !p.ValidateHost {
		return true
	}
	h := hostName(host)
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	return slices.ContainsFunc(p.AllowedHosts, func(a string) bool { return hostName(a) == h })
}

// OriginAllowed reports whether origin (an Origin header value) may call the
// API given the request's Host: it is allowlisted, or it names the same host
// and port the request was addressed to. Only http and https Origins count.
// Conduit serves plain HTTP and never terminates TLS itself, so an https
// Origin can only come from a TLS-terminating proxy in front of it and is
// accepted; there is no "request arrived over TLS" case to compare against. The Host comparison is only
// meaningful after HostAllowed, which is why CheckRequest runs them in that
// order.
func (p BrowserPolicy) OriginAllowed(origin, host string) bool {
	for _, a := range p.AllowedOrigins {
		if a == "*" || a == origin {
			return true
		}
	}
	// A real Origin is scheme://host[:port] and nothing else; anything with
	// userinfo, a path, a query or a fragment is not a browser's and is not
	// treated as same-origin.
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return u.Host != "" && u.User == nil && (u.Path == "" || u.Path == "/") &&
		u.RawQuery == "" && u.Fragment == "" && strings.EqualFold(u.Host, host)
}

// CheckRequest applies the Host and Origin rules to r. It returns nil if the
// request may proceed. It is shared by Guard and the WebSocket upgrader so
// the two cannot diverge. A request without an Origin header is not a
// cross-origin browser request and is allowed here; the Content-Type rule in
// Guard covers the browser form-post case.
func (p BrowserPolicy) CheckRequest(r *http.Request) error {
	if !p.HostAllowed(r.Host) {
		ce := conduiterr.New(CodeHostForbidden, "the Host header is not allowed for this API")
		ce.ConfigPath = ConfigPathAllowedHosts
		ce.Suggestion = "address the API as localhost, 127.0.0.1 or [::1], or add the host name to api.http.allowed-hosts"
		return ce
	}
	if origin := r.Header.Get("Origin"); origin != "" && !p.OriginAllowed(origin, r.Host) {
		ce := conduiterr.New(CodeOriginForbidden, "the request Origin is not allowed")
		ce.ConfigPath = ConfigPathAllowedOrigins
		ce.Suggestion = "serve the page from the API's own origin, or add its exact origin (scheme://host[:port]) to api.http.cors.allowed-origins"
		return ce
	}
	return nil
}

// CheckWebSocketOrigin is the websocket upgrader's CheckOrigin. A missing
// Origin (curl, the CLI) is allowed, as gorilla does.
func (p BrowserPolicy) CheckWebSocketOrigin(r *http.Request) bool {
	return p.CheckRequest(r) == nil
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// jsonContentType reports whether r satisfies the Content-Type rule. A
// request with no body and no Content-Type (for example `POST
// /v1/pipelines/{id}/start`) is allowed: it cannot carry attacker-chosen
// content, and a cross-origin one is already refused on Origin.
func jsonContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
	}
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

// Guard wraps next with the browser protections: Host validation (loopback
// binds), the Origin rule (every method, including OPTIONS and WebSocket
// upgrades) and the application/json rule for mutating methods. Refusals are
// 403 and 415 responses carrying a stable error code.
//
// Why Content-Type: a cross-site HTML form or `fetch` with `text/plain` is a
// "simple request" that a browser sends without a preflight, and the gateway
// would otherwise decode it as JSON. Requiring application/json forces a
// preflight, which only allowlisted origins pass.
func (p BrowserPolicy) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := p.CheckRequest(r); err != nil {
			if ce, ok := conduiterr.Get(err); ok {
				p.Rejections.Record(r.Context(), ce.Code, r.RemoteAddr, "rejected HTTP request")
			}
			writeHTTPError(w, http.StatusForbidden, err)
			return
		}
		if isMutating(r.Method) && !jsonContentType(r) {
			ce := conduiterr.New(CodeContentTypeUnsupported, "mutating requests must use Content-Type: application/json")
			ce.Suggestion = "send the request body as JSON with the header `Content-Type: application/json`"
			p.Rejections.Record(r.Context(), CodeContentTypeUnsupported, r.RemoteAddr, "rejected HTTP request")
			writeHTTPError(w, http.StatusUnsupportedMediaType, ce)
			return
		}
		next.ServeHTTP(w, r)
	})
}
