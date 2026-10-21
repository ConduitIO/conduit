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
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/matryer/is"
)

// These tests are the regression tests for cross-site request forgery and DNS
// rebinding against the HTTP API. Each case is a request a malicious web page
// (or a rebinding server) can really make, sent over a real socket to a real
// Runtime. "created" is read from the engine, not from the response, so a
// request that was answered with an error but still had an effect fails.

const createPipelineBody = `{"config":{"name":"csrf-victim"}}`

func TestCSRF_CrossOriginRequests(t *testing.T) {
	a := startSecureAPI(t, nil)
	port := a.port(t)
	selfOrigin := "http://127.0.0.1:" + port

	testCases := []struct {
		name        string
		method      string
		path        string
		body        string
		headers     map[string]string
		wantCode    int
		wantCreated int // pipelines in the engine afterwards
	}{
		{
			name:   "cross-origin POST text/plain (simple request, no preflight)",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"},
			wantCode: 403,
		},
		{
			name:   "cross-origin POST application/json",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"},
			wantCode: 403,
		},
		{
			name:   "cross-origin POST urlencoded form",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://evil.example", "Content-Type": "application/x-www-form-urlencoded"},
			wantCode: 403,
		},
		{
			name:   "cross-origin POST multipart form",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://evil.example", "Content-Type": "multipart/form-data; boundary=x"},
			wantCode: 403,
		},
		{
			name:   "opaque origin (sandboxed iframe, data: URL)",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "null", "Content-Type": "text/plain"},
			wantCode: 403,
		},
		{
			name:   "cross-origin POST with no body (start/stop style)",
			method: "POST", path: "/v1/pipelines/nope/start",
			headers:  map[string]string{"Origin": "http://evil.example"},
			wantCode: 403,
		},
		{
			name:   "cross-origin DELETE",
			method: "DELETE", path: "/v1/pipelines/nope",
			headers:  map[string]string{"Origin": "http://evil.example"},
			wantCode: 403,
		},
		{
			name:   "cross-origin PUT",
			method: "PUT", path: "/v1/pipelines/nope", body: `{"config":{"name":"x"}}`,
			headers:  map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"},
			wantCode: 403,
		},
		{
			name:   "cross-origin GET is refused too (no read-side leak)",
			method: "GET", path: "/v1/pipelines",
			headers:  map[string]string{"Origin": "http://evil.example"},
			wantCode: 403,
		},
		{
			name:   "cross-origin GET of metrics",
			method: "GET", path: "/metrics",
			headers:  map[string]string{"Origin": "http://evil.example"},
			wantCode: 403,
		},
		{
			name:   "cross-origin preflight from a stranger",
			method: "OPTIONS", path: "/v1/pipelines",
			headers:  map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "POST"},
			wantCode: 403,
		},
		{
			name:   "a different port on localhost is a different origin",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://127.0.0.1:1", "Content-Type": "application/json"},
			wantCode: 403,
		},
		{
			name:   "localhost and 127.0.0.1 are different origins",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Origin": "http://localhost:" + port, "Content-Type": "application/json"},
			wantCode: 403,
		},
		{
			// A browser form POST cannot omit Origin today, but old browsers and
			// some privacy modes strip it. The Content-Type rule is the backstop:
			// a form can only send these three types.
			name:   "no Origin, text/plain form post",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Content-Type": "text/plain"},
			wantCode: 415,
		},
		{
			name:   "no Origin, urlencoded form post",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			wantCode: 415,
		},
		{
			name:   "no Origin, multipart form post",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Content-Type": "multipart/form-data; boundary=x"},
			wantCode: 415,
		},
		{
			name:   "no Origin, body with no Content-Type",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			wantCode: 415,
		},
		{
			name:   "no Origin, json-looking but wrong type",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Content-Type": "application/jsonp"},
			wantCode: 415,
		},
		{
			name:   "curl-style JSON POST still works",
			method: "POST", path: "/v1/pipelines", body: createPipelineBody,
			headers:  map[string]string{"Content-Type": "application/json"},
			wantCode: 200, wantCreated: 1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			before := a.pipelineCount()
			code, body := a.do(t, tc.method, tc.path, tc.body, tc.headers)
			is.Equal(code, tc.wantCode)
			if tc.wantCode == 403 || tc.wantCode == 415 {
				is.True(strings.Contains(body, `"reason"`) && strings.Contains(body, "api."))
				is.True(strings.Contains(body, "suggestion"))
				is.Equal(a.pipelineCount(), before) // refused means no effect
			}
			if tc.wantCreated > 0 {
				is.Equal(a.pipelineCount(), before+tc.wantCreated)
			}
		})
	}

	t.Run("same-origin requests with an Origin header work", func(t *testing.T) {
		is := is.New(t)
		before := a.pipelineCount()
		code, _ := a.do(t, "POST", "/v1/pipelines", `{"config":{"name":"same-origin"}}`,
			map[string]string{"Origin": selfOrigin, "Content-Type": "application/json; charset=utf-8"})
		is.Equal(code, 200)
		is.Equal(a.pipelineCount(), before+1)
		code, _ = a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Origin": selfOrigin})
		is.Equal(code, 200)
	})

	t.Run("body-less lifecycle calls need no Content-Type", func(t *testing.T) {
		is := is.New(t)
		code, _ := a.do(t, "POST", "/v1/pipelines/does-not-exist/start", "", nil)
		is.Equal(code, 404) // reached the handler; not 415
		code, _ = a.do(t, "DELETE", "/v1/pipelines/does-not-exist", "", nil)
		is.Equal(code, 404)
	})
}

func TestCSRF_AllowlistedOrigin(t *testing.T) {
	a := startSecureAPI(t, func(c *Config) {
		c.API.HTTP.CORS.AllowedOrigins = []string{"https://console.example"}
	})
	is := is.New(t)

	// Preflight succeeds for the allowlisted origin and carries the CORS headers.
	req := map[string]string{"Origin": "https://console.example", "Access-Control-Request-Method": "POST"}
	code, _ := a.do(t, "OPTIONS", "/v1/pipelines", "", req)
	is.Equal(code, 200)

	// The real request is served.
	before := a.pipelineCount()
	code, _ = a.do(t, "POST", "/v1/pipelines", createPipelineBody,
		map[string]string{"Origin": "https://console.example", "Content-Type": "application/json"})
	is.Equal(code, 200)
	is.Equal(a.pipelineCount(), before+1)

	// Allowlisting one origin does not open the others, and the allowlisted
	// origin still cannot use the simple-request content types.
	code, _ = a.do(t, "POST", "/v1/pipelines", createPipelineBody,
		map[string]string{"Origin": "https://other.example", "Content-Type": "application/json"})
	is.Equal(code, 403)
	code, _ = a.do(t, "POST", "/v1/pipelines", createPipelineBody,
		map[string]string{"Origin": "https://console.example", "Content-Type": "text/plain"})
	is.Equal(code, 415)
}

func TestDNSRebinding_Host(t *testing.T) {
	a := startSecureAPI(t, func(c *Config) { c.API.HTTP.AllowedHosts = []string{"conduit.internal"} })
	port := a.port(t)

	t.Run("rebound host is refused, with and without a same-origin Origin", func(t *testing.T) {
		is := is.New(t)
		host := "attacker.example:" + port
		// A rebinding page's requests look same-origin to the browser: Origin
		// and Host are both the attacker's name.
		for _, h := range []map[string]string{
			{"Host": host},
			{"Host": host, "Origin": "http://" + host},
			{"Host": host, "Origin": "http://" + host, "Content-Type": "application/json"},
		} {
			before := a.pipelineCount()
			code, body := a.do(t, "POST", "/v1/pipelines", createPipelineBody, withDefault(h, "Content-Type", "application/json"))
			is.Equal(code, 403)
			is.True(strings.Contains(body, "api.host_forbidden"))
			is.True(strings.Contains(body, "api.http.allowed-hosts"))
			is.Equal(a.pipelineCount(), before)
		}
		code, _ := a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": host, "Origin": "http://" + host})
		is.Equal(code, 403)
		code, _ = a.do(t, "GET", "/metrics", "", map[string]string{"Host": host})
		is.Equal(code, 403)
	})

	t.Run("lookalike hosts are refused", func(t *testing.T) {
		for _, h := range []string{
			"localhost.attacker.example", "127.0.0.1.attacker.example", "attacker-localhost",
			"localhost.", "127.0.0.2", "0.0.0.0", "[::2]", "LOCALHOST.evil.example", "localhost@evil.example",
		} {
			code, _ := a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": h + ":" + port})
			if code != 403 && code != 400 {
				t.Errorf("Host %q: status %d, want 403", h, code)
			}
		}
	})

	t.Run("loopback names and configured names are served", func(t *testing.T) {
		is := is.New(t)
		for _, h := range []string{"localhost", "LocalHost", "127.0.0.1", "[::1]", "conduit.internal"} {
			code, _ := a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": h + ":" + port})
			if code != 200 {
				t.Errorf("Host %q: status %d, want 200", h, code)
			}
			is.True(true)
		}
		code, _ := a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": "LocalHost"}) // no port
		is.Equal(code, 200)
	})

	t.Run("UI static assets are host-checked too", func(t *testing.T) {
		is := is.New(t)
		code, _ := a.do(t, "GET", "/", "", map[string]string{"Host": "attacker.example:" + port})
		is.Equal(code, 403)
	})
}

func TestDNSRebinding_NotAppliedToTokenProtectedNetworkBind(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, func(c *Config) {
		c.API.HTTP.Address = "0.0.0.0:0"
		c.API.GRPC.Address = "0.0.0.0:0"
		c.API.Token = testToken
	})
	// On a network bind the legitimate Host names are unknowable; the token is
	// what protects the API.
	code, _ := a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": "conduit.prod.example", "Authorization": "Bearer " + testToken})
	is.Equal(code, 200)
	code, _ = a.do(t, "GET", "/v1/pipelines", "", map[string]string{"Host": "conduit.prod.example"})
	is.Equal(code, 401)
}

// A browser that holds the token (say, pasted into a console) is still not a
// licence for another origin to use it.
func TestCSRF_TokenDoesNotWaiveOriginChecks(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, withToken)
	code, _ := a.do(t, "POST", "/v1/pipelines", createPipelineBody, map[string]string{
		"Origin": "http://evil.example", "Content-Type": "application/json", "Authorization": "Bearer " + testToken,
	})
	is.Equal(code, 403)
	is.Equal(a.pipelineCount(), 0)
}

func TestInspectorWebSocket_Origins(t *testing.T) {
	a := startSecureAPI(t, nil)
	port := a.port(t)
	target := "ws://" + a.httpAddr + "/v1/connectors/none/inspect"

	dial := func(h http.Header) (*websocket.Conn, int) {
		conn, resp, err := websocket.DefaultDialer.Dial(target, h)
		if err != nil {
			if resp == nil {
				t.Fatalf("dial failed without a response: %v", err)
			}
			defer resp.Body.Close()
			return nil, resp.StatusCode
		}
		return conn, resp.StatusCode
	}

	t.Run("cross-origin upgrade is refused", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial(http.Header{"Origin": {"http://evil.example"}})
		is.True(conn == nil)
		is.Equal(code, 403)
	})
	t.Run("opaque origin is refused", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial(http.Header{"Origin": {"null"}})
		is.True(conn == nil)
		is.Equal(code, 403)
	})
	t.Run("rebinding: attacker Host and matching attacker Origin is refused", func(t *testing.T) {
		is := is.New(t)
		host := "attacker.example:" + port
		conn, code := dial(http.Header{"Origin": {"http://" + host}, "Host": {host}})
		is.True(conn == nil)
		is.Equal(code, 403)
	})
	t.Run("same-origin upgrade works", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial(http.Header{"Origin": {"http://127.0.0.1:" + port}})
		is.True(conn != nil)
		is.Equal(code, 101)
		_ = conn.Close()
	})
	t.Run("no Origin (CLI, curl) works", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial(nil)
		is.True(conn != nil)
		is.Equal(code, 101)
		_ = conn.Close()
	})
}

func TestInspectorWebSocket_AllowlistedOrigin(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, func(c *Config) { c.API.HTTP.CORS.AllowedOrigins = []string{"https://console.example"} })
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+a.httpAddr+"/v1/connectors/none/inspect", http.Header{"Origin": {"https://console.example"}})
	is.NoErr(err)
	is.Equal(resp.StatusCode, 101)
	_ = conn.Close()
	resp.Body.Close()
	_, resp, err = websocket.DefaultDialer.Dial("ws://"+a.httpAddr+"/v1/connectors/none/inspect", http.Header{"Origin": {"https://other.example"}})
	is.True(err != nil)
	is.Equal(resp.StatusCode, 403)
	resp.Body.Close()
}

func withDefault(h map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for hk, hv := range h {
		out[hk] = hv
	}
	if _, ok := out[k]; !ok {
		out[k] = v
	}
	return out
}
