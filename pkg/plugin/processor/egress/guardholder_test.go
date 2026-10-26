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

package egress

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// TestGuardHolder_DefaultBlocksThenExempts: a processor that never receives a
// guard uses the default one, which refuses loopback; SetDialGuard swaps in
// the operator's guard and drops the cached client so the next request uses it.
func TestGuardHolder_DefaultBlocksThenExempts(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var h GuardHolder
	is.True(h.Guard() == nil)

	c := h.HTTPClient()
	is.True(c == h.HTTPClient()) // built once and reused
	_, err := guardedGet(t, c, srv.URL)
	is.True(err != nil) // default guard refuses loopback

	e, err := ParseAllowEntry("http://" + srv.Listener.Addr().String())
	is.NoErr(err)
	g := NewDialGuard([]AllowEntry{e})
	h.SetDialGuard(g)
	is.Equal(h.Guard(), g)

	c2 := h.HTTPClient()
	is.True(c2 != c) // cache dropped by SetDialGuard
	status, err := guardedGet(t, c2, srv.URL)
	is.NoErr(err)
	is.Equal(status, http.StatusNoContent)
}

// TestStripReservedKeys removes the whole host-reserved sdk.egress. namespace,
// including keys added after the four known ones, and nothing else.
func TestStripReservedKeys(t *testing.T) {
	is := is.New(t)
	settings := map[string]string{
		"sdk.egress.allow":      "https://example.com",
		"sdk.egress.future-key": "x",
		"sdk.egressish":         "kept: not the namespace",
		"url":                   "https://example.com",
	}
	StripReservedKeys(settings)
	is.Equal(settings, map[string]string{
		"sdk.egressish": "kept: not the namespace",
		"url":           "https://example.com",
	})
	StripReservedKeys(nil) // a nil map is a no-op
}

// TestErrorMessages pins the dial refusal and DNS error texts, which carry the
// audit reason and are matched by operators' log alerts.
func TestErrorMessages(t *testing.T) {
	is := is.New(t)
	refused := &dialRefusedError{ip: net.ParseIP("10.0.0.1"), port: "443", reason: refusedReason("private")}
	is.Equal(refused.Error(), "egress: dial to 10.0.0.1:443 refused (private)")

	inner := &net.DNSError{Err: "no such host", Name: "nope.invalid"}
	dnsErr := &dnsError{err: inner}
	is.True(strings.HasPrefix(dnsErr.Error(), "egress: DNS resolution failed: "))
	is.Equal(dnsErr.Unwrap(), error(inner))
}

// get issues a GET with a context and closes the body, so a refused request
// returns only its error.
func guardedGet(t *testing.T, c *http.Client, url string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
