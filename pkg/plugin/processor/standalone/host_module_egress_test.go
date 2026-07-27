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

package standalone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/pprocutils/v1/toproto"
	procutilsv1 "github.com/conduitio/conduit-processor-sdk/proto/procutils/v1"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
	"github.com/stealthrocket/wazergo/types"
	"google.golang.org/protobuf/proto"
)

// newEgressTestInstance builds a bare hostModuleInstance wired only with the
// per-instance egress service, so http_request can be exercised at the host-ABI
// layer without a compiled WASM guest.
func newEgressTestInstance(policy egress.Policy) *hostModuleInstance {
	return &hostModuleInstance{
		logger:           log.Nop(),
		httpService:      egress.New(policy, log.Nop()),
		parkedResponses:  make(map[string]proto.Message),
		lastRequestBytes: make(map[string][]byte),
	}
}

// callHTTPRequest drives the exact two-try park/resize protocol the guest uses
// (wasm/caller.go): first call with a tight buffer, resize once on the returned
// size, second call fills it. Returns either the decoded response or the numeric
// error code the guest would receive.
func callHTTPRequest(t *testing.T, m *hostModuleInstance, req *procutilsv1.HTTPRequest) (*procutilsv1.HTTPResponse, uint32) {
	t.Helper()
	reqBytes, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := types.Bytes(append([]byte(nil), reqBytes...))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		size := uint32(m.httpRequest(ctx, buf))
		switch {
		case size >= pprocutils.ErrorCodeStart:
			return nil, size
		case int(size) > len(buf) && i == 0:
			// resize, preserving the request prefix so handleWasmRequest sees the
			// same request and returns the parked response.
			buf = types.Bytes(append([]byte(buf), make([]byte, int(size)-len(buf))...))
			continue
		default:
			var resp procutilsv1.HTTPResponse
			if err := proto.Unmarshal(buf[:size], &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			return &resp, 0
		}
	}
	t.Fatal("buffer not resized correctly")
	return nil, 0
}

func hp(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func allowPolicy(t *testing.T, spec string) egress.Policy {
	t.Helper()
	al, err := egress.ParseAllowlist(spec)
	if err != nil {
		t.Fatal(err)
	}
	return egress.Policy{Enabled: true, Allowlist: al, MaxResponseBytes: egress.DefaultMaxResponseBytes}
}

// TestHostModule_HTTPRequest_RoundTrip proves the http_request host export
// marshals/unmarshals the HTTPRequest/HTTPResponse proto pair over the existing
// park/resize protocol and performs the gated call.
func TestHostModule_HTTPRequest_RoundTrip(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Method)
		_, _ = w.Write([]byte("hello-guest"))
	}))
	defer srv.Close()

	m := newEgressTestInstance(allowPolicy(t, "http://"+hp(t, srv)))
	resp, errCode := callHTTPRequest(t, m, toproto.HTTPRequest(pprocutils.HTTPRequest{
		Method: "GET", URL: srv.URL,
	}))
	is.Equal(errCode, uint32(0))
	is.Equal(int(resp.StatusCode), 200)
	is.Equal(string(resp.Body), "hello-guest")
}

// TestHostModule_HTTPRequest_Disabled proves a nil httpService (egress never
// wired) yields the deny-all ABI code, not a panic.
func TestHostModule_HTTPRequest_Disabled(t *testing.T) {
	is := is.New(t)
	m := &hostModuleInstance{
		logger:           log.Nop(),
		parkedResponses:  make(map[string]proto.Message),
		lastRequestBytes: make(map[string][]byte),
	}
	_, errCode := callHTTPRequest(t, m, toproto.HTTPRequest(pprocutils.HTTPRequest{Method: "GET", URL: "https://api.openai.com/v1"}))
	is.Equal(errCode, uint32(pprocutils.ErrorCodeHTTPEgressDisabled))
}

// TestHostModule_HTTPRequest_MultiTenantIsolation is MUST-FIX 6 at the host-module
// binding layer: two instances with two different per-processor policies, each
// bound to its own httpService. Neither instance can reach the other's granted
// host — proving the policy is per-hostModuleInstance state (bound at
// newWASMProcessor / hostModuleOptions), never a shared Registry field.
func TestHostModule_HTTPRequest_MultiTenantIsolation(t *testing.T) {
	is := is.New(t)
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("A")) }))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("B")) }))
	defer srvB.Close()

	mA := newEgressTestInstance(allowPolicy(t, "http://"+hp(t, srvA)))
	mB := newEgressTestInstance(allowPolicy(t, "http://"+hp(t, srvB)))

	// each instance reaches only its own granted host
	rA, ec := callHTTPRequest(t, mA, toproto.HTTPRequest(pprocutils.HTTPRequest{Method: "GET", URL: srvA.URL}))
	is.Equal(ec, uint32(0))
	is.Equal(string(rA.Body), "A")

	rB, ec := callHTTPRequest(t, mB, toproto.HTTPRequest(pprocutils.HTTPRequest{Method: "GET", URL: srvB.URL}))
	is.Equal(ec, uint32(0))
	is.Equal(string(rB.Body), "B")

	// cross-tenant access is forbidden: no leakage of A's allowlist into B or vice versa
	_, ec = callHTTPRequest(t, mA, toproto.HTTPRequest(pprocutils.HTTPRequest{Method: "GET", URL: srvB.URL}))
	is.Equal(ec, uint32(pprocutils.ErrorCodeHTTPForbidden))
	_, ec = callHTTPRequest(t, mB, toproto.HTTPRequest(pprocutils.HTTPRequest{Method: "GET", URL: srvA.URL}))
	is.Equal(ec, uint32(pprocutils.ErrorCodeHTTPForbidden))
}

// TestHostModule_HTTPRequest_EnvSecretInjection proves the host-injected
// credential path end-to-end at the host-ABI layer with the real
// EnvSecretResolver: the guest names a granted secret (AuthSecretRef) and sets
// NO Authorization header itself; the host reads the value from the namespaced
// process env and injects it, and the destination server receives it. This is
// the wiring newWASMProcessor uses in production (egress.New(..., WithSecretResolver(EnvSecretResolver{}))).
func TestHostModule_HTTPRequest_EnvSecretInjection(t *testing.T) {
	is := is.New(t)
	t.Setenv("CONDUIT_SECRET_TEST_KEY", "Bearer sk-injected-by-host")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Policy grants the secret ref AND allowlists the local server. The resolver
	// is the real env-backed one, exactly as newWASMProcessor wires it.
	al, err := egress.ParseAllowlist("http://" + hp(t, srv))
	is.NoErr(err)
	policy := egress.Policy{
		Enabled:          true,
		Allowlist:        al,
		SecretRefs:       map[string]struct{}{"test_key": {}},
		MaxResponseBytes: egress.DefaultMaxResponseBytes,
	}
	m := &hostModuleInstance{
		logger:           log.Nop(),
		httpService:      egress.New(policy, log.Nop(), egress.WithSecretResolver(egress.EnvSecretResolver{})),
		parkedResponses:  make(map[string]proto.Message),
		lastRequestBytes: make(map[string][]byte),
	}

	// The guest supplies AuthSecretRef and NO Authorization header of its own.
	resp, ec := callHTTPRequest(t, m, toproto.HTTPRequest(pprocutils.HTTPRequest{
		Method:        "POST",
		URL:           srv.URL,
		AuthSecretRef: "test_key",
	}))
	is.Equal(ec, uint32(0))
	is.Equal(int(resp.StatusCode), 200)
	// The server saw the env-sourced credential, injected host-side.
	is.Equal(gotAuth, "Bearer sk-injected-by-host")

	// An ungranted ref is refused before any resolution (defense in depth), even
	// if its env var happens to exist.
	t.Setenv("CONDUIT_SECRET_UNGRANTED", "Bearer nope")
	_, ec = callHTTPRequest(t, m, toproto.HTTPRequest(pprocutils.HTTPRequest{
		Method:        "POST",
		URL:           srv.URL,
		AuthSecretRef: "ungranted",
	}))
	is.Equal(ec, uint32(pprocutils.ErrorCodeHTTPForbidden))
}
