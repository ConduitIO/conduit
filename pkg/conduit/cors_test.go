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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
	"github.com/matryer/is"
)

func TestOriginAllowed(t *testing.T) {
	testCases := []struct {
		name    string
		origin  string
		allowed []string
		want    bool
	}{
		{"empty allowlist denies", "http://localhost:5173", nil, false},
		{"exact match", "http://localhost:5173", []string{"http://localhost:5173"}, true},
		{"no match among several", "http://evil.example", []string{"http://a.example", "http://b.example"}, false},
		{"one of several", "http://b.example", []string{"http://a.example", "http://b.example"}, true},
		{"wildcard allows any", "http://anything.example", []string{"*"}, true},
		{"case-sensitive host mismatch", "http://LocalHost:5173", []string{"http://localhost:5173"}, false},
		{"trailing slash configured never matches a canonical origin", "http://localhost:5173", []string{"http://localhost:5173/"}, false},
		{"literal null only matches if configured", "null", []string{"http://localhost:5173"}, false},
		{"literal null matches when configured", "null", []string{"null"}, true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(originAllowed(tc.origin, tc.allowed), tc.want)
		})
	}
}

// ok is the handler allowCORS wraps; it records whether the request reached it
// (so we can assert non-preflight requests always pass through).
func newRecordingHandler() (http.Handler, *bool) {
	reached := false
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	return h, &reached
}

func TestAllowCORS_AllowedOrigin_ReflectsAndVaries(t *testing.T) {
	is := is.New(t)
	inner, reached := newRecordingHandler()
	h := allowCORS(inner, []string{"http://localhost:5173"})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/pipelines", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	is.Equal(rec.Header().Get("Access-Control-Allow-Origin"), "http://localhost:5173") // reflected, not "*"
	is.Equal(rec.Header().Get("Vary"), "Origin")
	is.Equal(rec.Header().Get("Access-Control-Allow-Credentials"), "") // never set
	is.True(*reached)                                                  // non-preflight passes through
}

func TestAllowCORS_DisallowedOrigin_NoHeadersButPassesThrough(t *testing.T) {
	is := is.New(t)
	inner, reached := newRecordingHandler()
	h := allowCORS(inner, []string{"http://localhost:5173"})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/pipelines", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	is.Equal(rec.Header().Get("Access-Control-Allow-Origin"), "") // no CORS header for a disallowed origin
	is.True(*reached)                                             // still served (non-browser clients unaffected)
}

func TestAllowCORS_Preflight_ReflectsRequestedHeadersAndSetsMaxAge(t *testing.T) {
	is := is.New(t)
	inner, reached := newRecordingHandler()
	h := allowCORS(inner, []string{"http://localhost:5173"})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/v1/pipelines", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "x-request-id,content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	is.Equal(rec.Header().Get("Access-Control-Allow-Origin"), "http://localhost:5173")
	is.Equal(rec.Header().Get("Access-Control-Allow-Headers"), "x-request-id,content-type") // reflected — X-Request-Id not blocked
	is.Equal(rec.Header().Get("Access-Control-Max-Age"), "600")
	is.True(rec.Header().Get("Access-Control-Allow-Methods") != "")
	is.True(!*reached) // a preflight is answered, not forwarded to the handler
}

func TestAllowCORS_Wildcard_ReflectsRequestOrigin(t *testing.T) {
	is := is.New(t)
	inner, _ := newRecordingHandler()
	h := allowCORS(inner, []string{"*"})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/pipelines", nil)
	req.Header.Set("Origin", "http://whatever.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	is.Equal(rec.Header().Get("Access-Control-Allow-Origin"), "http://whatever.example") // reflected, never literal "*"
	is.Equal(rec.Header().Get("Vary"), "Origin")
}

func TestAllowCORS_EmptyAllowlist_DeniesAll(t *testing.T) {
	is := is.New(t)
	inner, reached := newRecordingHandler()
	h := allowCORS(inner, nil)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/pipelines", nil)
	req.Header.Set("Origin", "http://localhost:4200") // the old hardcoded origin is no longer special
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	is.Equal(rec.Header().Get("Access-Control-Allow-Origin"), "")
	is.True(*reached)
}

func TestWSCheckOrigin(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	check := wsCheckOrigin(allowed)

	// host is the request Host, so "http://<host>" is a same-origin request.
	const host = "conduit.internal:8080"
	newReq := func(origin string) *http.Request {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/connectors/x/inspect", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	testCases := []struct {
		name   string
		origin string
		want   bool
	}{
		{"no origin (curl/CLI) allowed", "", true},
		{"allowed cross-origin", "http://localhost:5173", true},
		{"disallowed cross-origin rejected", "http://evil.example", false},
		// Same-origin must be allowed even though it is NOT in the (empty of
		// this host) allowlist — browsers send Origin on same-origin WS too, and
		// this is the embedded UI's case. Regression guard for the same-origin bug.
		{"same-origin allowed regardless of allowlist", "http://" + host, true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(check(newReq(tc.origin)), tc.want)
		})
	}

	// For a cross-origin request, wsCheckOrigin agrees with originAllowed, so the
	// HTTP and WS surfaces never diverge on the allowlist decision.
	is.New(t).Equal(check(newReq("http://localhost:5173")), originAllowed("http://localhost:5173", allowed))
}

func TestWSCheckOrigin_WildcardAllowsAny(t *testing.T) {
	is := is.New(t)
	check := wsCheckOrigin([]string{"*"})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/connectors/x/inspect", nil)
	r.Header.Set("Origin", "http://whatever.example")
	is.True(check(r))
}

// TestBuildAPIHandler_WSOriginWired proves the runtime wiring itself, not just
// the pieces: buildAPIHandler must apply wsCheckOrigin from the allowlist to the
// websocket upgrade path. A regression that passed nil (or omitted wsCheckOrigin)
// here would pass every other unit test but is caught by a real cross-origin
// upgrade being refused with 403 while an allowlisted one succeeds.
func TestBuildAPIHandler_WSOriginWired(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	// The inner (non-websocket) handler streams a line the proxy relays once a WS
	// upgrade succeeds.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("hi\n"))
		is.NoErr(err)
	})
	h := buildAPIHandler(ctx, inner, []string{"http://allowed.example"}, log.Nop())
	s := httptest.NewServer(h)
	defer s.Close()
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	// Disallowed cross-origin upgrade -> refused with 403 (proves wsCheckOrigin
	// is wired into the upgrader by buildAPIHandler).
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://evil.example"}})
	is.True(err != nil)
	if resp != nil {
		is.Equal(resp.StatusCode, http.StatusForbidden)
		resp.Body.Close()
	}

	// Allowlisted cross-origin upgrade -> succeeds.
	ws, resp2, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://allowed.example"}})
	is.NoErr(err)
	defer ws.Close()
	defer resp2.Body.Close()
}

func TestIsLoopbackBind(t *testing.T) {
	testCases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.2:8080", true}, // all of 127.0.0.0/8 is loopback
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false},        // all interfaces
		{"0.0.0.0:8080", false}, // all interfaces
		{"192.168.1.10:8080", false},
	}
	for _, tc := range testCases {
		t.Run(tc.addr, func(t *testing.T) {
			is := is.New(t)
			is.Equal(isLoopbackBind(tc.addr), tc.want)
		})
	}
}
