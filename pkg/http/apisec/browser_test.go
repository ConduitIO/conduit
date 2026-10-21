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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestBrowserPolicy_HostAllowed(t *testing.T) {
	p := BrowserPolicy{ValidateHost: true, AllowedHosts: []string{"Conduit.Internal", "[fe80::1]"}}
	for host, want := range map[string]bool{
		"localhost":                  true,
		"localhost:8080":             true,
		"LOCALHOST:8080":             true,
		"127.0.0.1:8080":             true,
		"[::1]:8080":                 true,
		"[::1]":                      true,
		"conduit.internal":           true,
		"conduit.internal:9":         true,
		"fe80::1":                    true,
		"attacker.example":           false,
		"attacker.example:8080":      false,
		"localhost.attacker.example": false,
		"127.0.0.1.attacker.example": false,
		"localhost.":                 false,
		"127.0.0.2":                  false,
		"0.0.0.0":                    false,
		"":                           false,
	} {
		if got := p.HostAllowed(host); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
	// With validation off (network bind) every host is accepted.
	is.New(t).True(BrowserPolicy{}.HostAllowed("anything.example"))
}

func TestBrowserPolicy_OriginAllowed(t *testing.T) {
	p := BrowserPolicy{AllowedOrigins: []string{"https://console.example"}}
	for _, tc := range []struct {
		origin, host string
		want         bool
	}{
		{"https://console.example", "127.0.0.1:8080", true},
		{"http://127.0.0.1:8080", "127.0.0.1:8080", true}, // same-origin
		{"http://127.0.0.1:8081", "127.0.0.1:8080", false},
		{"http://evil.example", "127.0.0.1:8080", false},
		{"null", "127.0.0.1:8080", false},
		{"", "127.0.0.1:8080", false},
		{"http://", "", false}, // host-less origin never matches an empty Host
		{"%zz", "127.0.0.1:8080", false},
		{"https://console.example.evil.example", "127.0.0.1:8080", false},
		{"https://console.example/", "127.0.0.1:8080", false}, // not canonical
	} {
		if got := p.OriginAllowed(tc.origin, tc.host); got != tc.want {
			t.Errorf("OriginAllowed(%q, %q) = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
	is.New(t).True(BrowserPolicy{AllowedOrigins: []string{"*"}}.OriginAllowed("http://anything.example", "127.0.0.1:8080"))
	is.New(t).True(BrowserPolicy{AllowedOrigins: []string{"null"}}.OriginAllowed("null", "127.0.0.1:8080"))
}

func TestBrowserPolicy_CheckRequest_HostIsCheckedBeforeSameOrigin(t *testing.T) {
	is := is.New(t)
	p := BrowserPolicy{ValidateHost: true}
	r := httptest.NewRequest("GET", "/v1/pipelines", nil)
	r.Host = "attacker.example:8080"
	r.Header.Set("Origin", "http://attacker.example:8080") // same-origin by Host comparison
	ce := p.CheckRequest(r)
	is.True(ce != nil)
	is.Equal(ce.Code, CodeHostForbidden)
	is.Equal(ce.ConfigPath, ConfigPathAllowedHosts)
	is.True(ce.Suggestion != "")
	is.True(!p.CheckWebSocketOrigin(r))
}

func TestBrowserPolicy_Guard_ContentType(t *testing.T) {
	p := BrowserPolicy{Logger: log.Nop()}
	next, calls := okHandler()
	h := p.Guard(next)

	type req struct {
		method, ct, body string
		chunked          bool
	}
	for name, tc := range map[string]struct {
		req  req
		want int
	}{
		"json":                 {req{"POST", "application/json", "{}", false}, 200},
		"json with charset":    {req{"POST", "application/json; charset=utf-8", "{}", false}, 200},
		"JSON upper case":      {req{"POST", "Application/JSON", "{}", false}, 200},
		"text/plain":           {req{"POST", "text/plain", "{}", false}, 415},
		"text/plain;json":      {req{"POST", "text/plain; x=application/json", "{}", false}, 415},
		"form":                 {req{"POST", "application/x-www-form-urlencoded", "a=b", false}, 415},
		"multipart":            {req{"PUT", "multipart/form-data; boundary=x", "x", false}, 415},
		"garbage":              {req{"POST", "application/json;;;=", "{}", false}, 415},
		"body without type":    {req{"POST", "", "{}", false}, 415},
		"chunked without type": {req{"POST", "", "{}", true}, 415},
		"empty POST":           {req{"POST", "", "", false}, 200},
		"empty DELETE":         {req{"DELETE", "", "", false}, 200},
		"PATCH text":           {req{"PATCH", "text/plain", "{}", false}, 415},
		"GET needs no type":    {req{"GET", "", "", false}, 200},
		"GET with form body":   {req{"GET", "text/plain", "x", false}, 200},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			*calls = 0
			var body io.Reader
			if tc.req.body != "" {
				body = strings.NewReader(tc.req.body)
			}
			r := httptest.NewRequest(tc.req.method, "/v1/pipelines", body)
			if tc.req.chunked {
				r.ContentLength = -1
			}
			if tc.req.ct != "" {
				r.Header.Set("Content-Type", tc.req.ct)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			is.Equal(rec.Code, tc.want)
			if tc.want == 415 {
				is.Equal(*calls, 0)
				is.True(strings.Contains(rec.Body.String(), "api.content_type_unsupported"))
			}
		})
	}
}

func TestBrowserPolicy_Guard_OriginAppliesToEveryMethod(t *testing.T) {
	p := BrowserPolicy{Logger: log.Nop()}
	next, calls := okHandler()
	h := p.Guard(next)
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		r := httptest.NewRequest(m, "/v1/pipelines", nil)
		r.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", m, rec.Code)
		}
	}
	is.New(t).Equal(*calls, 0)
}
