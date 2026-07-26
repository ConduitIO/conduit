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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// --- test helpers ---------------------------------------------------------

type funcResolver func(host string) ([]net.IP, error)

func (f funcResolver) LookupIP(_ context.Context, host string) ([]net.IP, error) { return f(host) }

func staticResolver(ips ...net.IP) Resolver {
	return funcResolver(func(string) ([]net.IP, error) { return ips, nil })
}

// seqResolver returns a different answer on each successive lookup, to simulate
// a DNS rebind (public answer first, private answer next). Guarded by a mutex
// because the transport may resolve from more than one goroutine.
type seqResolver struct {
	mu      sync.Mutex
	answers [][]net.IP
	i       int
}

func (r *seqResolver) LookupIP(context.Context, string) ([]net.IP, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.answers[r.i]
	if r.i < len(r.answers)-1 {
		r.i++
	}
	return a, nil
}

func hostPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// trustTLS makes the egress client trust the httptest TLS server's self-signed
// cert (white-box: reach into the transport). Only used for https tests.
func trustTLS(t *testing.T, s *Service, srv *httptest.Server) {
	t.Helper()
	tr := s.client.Transport.(*http.Transport)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func mustAllow(t *testing.T, spec string) Policy {
	t.Helper()
	allow, err := ParseAllowlist(spec)
	if err != nil {
		t.Fatalf("parse allowlist %q: %v", spec, err)
	}
	return Policy{Enabled: true, Allowlist: allow, Timeout: 5 * time.Second, MaxResponseBytes: DefaultMaxResponseBytes}
}

// --- tests ----------------------------------------------------------------

func TestService_DenyAll(t *testing.T) {
	is := is.New(t)
	s := New(DenyAll(), log.Nop())
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: "https://api.openai.com/v1"})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPEgressDisabled))
}

func TestService_Stage1_Allowlist(t *testing.T) {
	is := is.New(t)
	s := New(mustAllow(t, "api.openai.com:443"), log.Nop())
	ctx := context.Background()

	cases := []struct {
		name string
		url  string
		want *pprocutils.Error
	}{
		{"host not allowlisted", "https://evil.example.com/x", pprocutils.ErrHTTPForbidden},
		{"port not allowlisted", "https://api.openai.com:8443/x", pprocutils.ErrHTTPForbidden},
		{"scheme rejected (ftp)", "ftp://api.openai.com/x", pprocutils.ErrHTTPInvalidRequest},
		{"scheme rejected (file)", "file:///etc/passwd", pprocutils.ErrHTTPInvalidRequest},
		{"http to https-only host", "http://api.openai.com/x", pprocutils.ErrHTTPForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			_, err := s.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: tc.url})
			is.True(cerrors.Is(err, tc.want))
		})
	}
}

// TestService_Rebinding_HTTPS is the headline DNS-rebinding test (MUST-FIX 4),
// run against a real https target: an allowlisted HOSTNAME (Stage 1 passes) that
// resolves to loopback is refused at the dial-time gate, even though a live TLS
// server is listening there. A custom-DialTLSContext bypass would connect and
// return 200; asserting ErrHTTPForbidden proves Control governs the TLS dial.
func TestService_Rebinding_HTTPS(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("SHOULD NOT REACH"))
	}))
	defer srv.Close()

	_, port, _ := net.SplitHostPort(hostPort(t, srv))
	// Allowlist the HOSTNAME (not an IP carve-out), so loopback is not admitted.
	p := mustAllow(t, "https://rebind.test:"+port)
	s := New(p, log.Nop(), WithResolver(staticResolver(srv.Listener.Addr().(*net.TCPAddr).IP)))
	trustTLS(t, s, srv)

	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: "https://rebind.test:" + port + "/v1"})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden)) // gate refused the loopback dial on https
}

// TestService_Rebinding_TOCTOU proves the gate re-evaluates per call at dial
// time: the SAME allowlisted hostname is allowed-by-gate when it resolves public
// and refused when it later resolves private.
func TestService_Rebinding_TOCTOU(t *testing.T) {
	is := is.New(t)
	p := mustAllow(t, "https://rebind.test:443")
	p.Timeout = 300 * time.Millisecond
	res := &seqResolver{answers: [][]net.IP{
		{net.ParseIP("203.0.113.10")}, // public (TEST-NET-3): gate allows, connect fails/times out
		{net.ParseIP("10.0.0.1")},     // private: gate refuses
	}}
	s := New(p, log.Nop(), WithResolver(res))
	ctx := context.Background()

	_, err1 := s.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: "https://rebind.test:443/"})
	is.True(!cerrors.Is(err1, pprocutils.ErrHTTPForbidden)) // gate ALLOWED public; failure is transport/timeout

	_, err2 := s.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: "https://rebind.test:443/"})
	is.True(cerrors.Is(err2, pprocutils.ErrHTTPForbidden)) // same host now refused (rebind defended)
}

// TestService_CarveOut_HTTPS_Positive proves the explicit (IP, port) carve-out
// admits a loopback https target end-to-end through the gate (the local-Ollama /
// private-VPC-endpoint case), and that the gated dial serves TLS.
func TestService_CarveOut_HTTPS_Positive(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s := New(mustAllow(t, "https://"+hostPort(t, srv)), log.Nop())
	trustTLS(t, s, srv)

	resp, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL + "/v1"})
	is.NoErr(err)
	is.Equal(resp.StatusCode, 200)
	is.Equal(string(resp.Body), "ok")
}

// TestService_CarveOut_PortScoped is the live (IP, port) carve-out scoping check
// (MUST-FIX 3): an allowlisted loopback:P is reachable, but the same IP on a
// different port is refused (it is not the listed pair).
func TestService_CarveOut_PortScoped(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ollama"))
	}))
	defer srv.Close()

	hp := hostPort(t, srv)
	_, port, _ := net.SplitHostPort(hp)
	s := New(mustAllow(t, "http://"+hp), log.Nop()) // only 127.0.0.1:<port>

	// listed pair works
	resp, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL})
	is.NoErr(err)
	is.Equal(resp.StatusCode, 200)

	// same IP, a DIFFERENT port (Redis-like) is refused
	otherPort := "6379"
	if port == "6379" {
		otherPort = "6380"
	}
	_, err = s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: "http://127.0.0.1:" + otherPort})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden))
}

// TestService_ProxyEnv_Ignored is MUST-FIX 1: HTTP_PROXY/HTTPS_PROXY/ALL_PROXY
// are ignored (Transport.Proxy pinned nil), so the client dials — and gates —
// the real destination instead of a proxy the gate never validated.
func TestService_ProxyEnv_Ignored(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer srv.Close()

	// A bogus proxy that would break the call if honored.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")

	s := New(mustAllow(t, "http://"+hostPort(t, srv)), log.Nop())
	resp, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL})
	is.NoErr(err) // proxy env ignored; real destination reached
	is.Equal(string(resp.Body), "direct")
}

// TestService_Redirect_Blocked: a 3xx to any Location is not followed.
func TestService_Redirect_Blocked(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("landing"))
	}))
	defer srv.Close()

	s := New(mustAllow(t, "http://"+hostPort(t, srv)), log.Nop())
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL + "/redir"})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden)) // redirect not followed -> no hop to metadata
}

func TestService_HeaderHardening(t *testing.T) {
	is := is.New(t)
	s := New(mustAllow(t, "api.openai.com:443"), log.Nop())
	ctx := context.Background()

	cases := []struct {
		name    string
		headers map[string][]string
	}{
		{"CRLF in value", map[string][]string{"X-Test": {"a\r\nInjected: 1"}}},
		{"CRLF in name", map[string][]string{"X-Test\r\nEvil": {"v"}}},
		{"guest Host override", map[string][]string{"Host": {"evil.example.com"}}},
		{"guest Authorization", map[string][]string{"Authorization": {"Bearer stolen"}}},
		{"guest Accept-Encoding", map[string][]string{"Accept-Encoding": {"gzip"}}},
		{"guest Content-Length", map[string][]string{"Content-Length": {"0"}}},
		{"guest pseudo-header", map[string][]string{":authority": {"evil"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			_, err := s.Do(ctx, pprocutils.HTTPRequest{
				Method:  "POST",
				URL:     "https://api.openai.com/v1",
				Headers: tc.headers,
			})
			is.True(cerrors.Is(err, pprocutils.ErrHTTPInvalidRequest))
		})
	}
}

// TestService_CredentialInjection is the mandatory host-injected-credential path:
// the guest names a secret, the host resolves and sets Authorization, and there
// is no guest-supplied-credential path.
func TestService_CredentialInjection(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	t.Run("host injects Authorization the guest never supplied", func(t *testing.T) {
		is := is.New(t)
		gotAuth = ""
		p := mustAllow(t, "http://"+hostPort(t, srv))
		p.SecretRefs = map[string]struct{}{"openai_api_key": {}}
		s := New(p, log.Nop(), WithSecretResolver(fakeSecrets{"openai_api_key": "Bearer SECRET-XYZ"}))
		resp, err := s.Do(context.Background(), pprocutils.HTTPRequest{
			Method: "GET", URL: srv.URL, AuthSecretRef: "openai_api_key",
		})
		is.NoErr(err)
		is.Equal(resp.StatusCode, 200)
		is.Equal(gotAuth, "Bearer SECRET-XYZ") // host set it from the secret ref
	})

	t.Run("ungranted secret ref is forbidden", func(t *testing.T) {
		is := is.New(t)
		p := mustAllow(t, "http://"+hostPort(t, srv)) // no SecretRefs granted
		s := New(p, log.Nop(), WithSecretResolver(fakeSecrets{"openai_api_key": "Bearer x"}))
		_, err := s.Do(context.Background(), pprocutils.HTTPRequest{
			Method: "GET", URL: srv.URL, AuthSecretRef: "openai_api_key",
		})
		is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden))
	})

	t.Run("secret ref with no backend wired fails non-silently", func(t *testing.T) {
		is := is.New(t)
		p := mustAllow(t, "http://"+hostPort(t, srv))
		p.SecretRefs = map[string]struct{}{"openai_api_key": {}}
		s := New(p, log.Nop()) // no WithSecretResolver
		_, err := s.Do(context.Background(), pprocutils.HTTPRequest{
			Method: "GET", URL: srv.URL, AuthSecretRef: "openai_api_key",
		})
		is.True(cerrors.Is(err, pprocutils.ErrHTTPInvalidRequest))
	})
}

type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, name string) (string, error) {
	v, ok := f[name]
	if !ok {
		return "", cerrors.New("no such secret")
	}
	return v, nil
}

// TestService_DecompressionBomb is MUST-FIX 5: with Accept-Encoding host-reserved,
// transparent decompression stays on and the size cap measures the DECOMPRESSED
// stream, so a small gzip body that inflates hugely trips the cap.
func TestService_DecompressionBomb(t *testing.T) {
	is := is.New(t)
	// ~200 KiB decompressed, tiny compressed.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(bytes.Repeat([]byte("A"), 200*1024))
	_ = gz.Close()
	bomb := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(bomb)
	}))
	defer srv.Close()

	p := mustAllow(t, "http://"+hostPort(t, srv))
	p.MaxResponseBytes = 4096 // cap well below the 200 KiB decompressed size
	s := New(p, log.Nop())

	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPResponseTooLarge)) // cap on decompressed stream
}

func TestService_OversizedResponse(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 100*1024))
	}))
	defer srv.Close()

	p := mustAllow(t, "http://"+hostPort(t, srv))
	p.MaxResponseBytes = 1024
	s := New(p, log.Nop())
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPResponseTooLarge))
}

func TestService_Timeout(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	p := mustAllow(t, "http://"+hostPort(t, srv))
	p.Timeout = 100 * time.Millisecond
	s := New(p, log.Nop())
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: srv.URL})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPTimeout))
}

// TestService_MultiTenantIsolation is MUST-FIX 6: two Services with two different
// allowlists on one host each refuse the OTHER's destination and admit only their
// own — the concrete proof that egress policy is per-Service (per-processor)
// state, not shared. Neither can reach the other's granted host.
func TestService_MultiTenantIsolation(t *testing.T) {
	is := is.New(t)
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("A")) }))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("B")) }))
	defer srvB.Close()

	sA := New(mustAllow(t, "http://"+hostPort(t, srvA)), log.Nop())
	sB := New(mustAllow(t, "http://"+hostPort(t, srvB)), log.Nop())
	ctx := context.Background()

	// each reaches only its own host
	rA, err := sA.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: srvA.URL})
	is.NoErr(err)
	is.Equal(string(rA.Body), "A")
	rB, err := sB.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: srvB.URL})
	is.NoErr(err)
	is.Equal(string(rB.Body), "B")

	// cross access is forbidden — no policy leakage between tenants
	_, err = sA.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: srvB.URL})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden))
	_, err = sB.Do(ctx, pprocutils.HTTPRequest{Method: "GET", URL: srvA.URL})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden))
}

// TestService_MultiAnswerDNS proves per-candidate gating (design scenario 6): a
// mixed answer set (one private, one admissible) skips the private candidate and
// dials only the admissible one — a private dial cannot be smuggled through on a
// fallback attempt.
func TestService_MultiAnswerDNS(t *testing.T) {
	is := is.New(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	defer ln.Close()
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	// carve-out the loopback:port so 127.0.0.1 is admissible on Stage 2.
	s := New(mustAllow(t, "http://127.0.0.1:"+port), log.Nop(),
		WithResolver(staticResolver(net.ParseIP("10.0.0.1"), net.ParseIP("127.0.0.1"))))
	base := &net.Dialer{Control: s.dialControl}

	conn, err := s.dialContext(base)(context.Background(), "tcp", "multi.test:"+port)
	is.NoErr(err) // private 10.0.0.1 skipped, admissible 127.0.0.1 dialed
	remoteIP := conn.RemoteAddr().(*net.TCPAddr).IP
	is.True(remoteIP.IsLoopback()) // dialed the loopback candidate, not the private one
	_ = conn.Close()

	// with NO admissible candidate, every private candidate is refused.
	s2 := New(Policy{Enabled: true, Allowlist: nil}, log.Nop(),
		WithResolver(staticResolver(net.ParseIP("10.0.0.1"), net.ParseIP("192.168.1.1"))))
	base2 := &net.Dialer{Control: s2.dialControl}
	_, err = s2.dialContext(base2)(context.Background(), "tcp", "multi.test:"+port)
	is.True(err != nil)
	var refused *dialRefusedError
	is.True(cerrors.As(err, &refused))
}

func TestService_DNSFailure(t *testing.T) {
	is := is.New(t)
	p := mustAllow(t, "https://noexist.test:443")
	s := New(p, log.Nop(), WithResolver(funcResolver(func(string) ([]net.IP, error) {
		return nil, cerrors.New("no such host")
	})))
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{Method: "GET", URL: "https://noexist.test:443/"})
	is.True(cerrors.Is(err, pprocutils.ErrHTTPDNS))
}

// TestService_MetadataIPLiteral is scenario 2: a direct request to the metadata
// IP literal is refused (Stage 1 miss, and Stage 2 would refuse it too).
func TestService_MetadataIPLiteral(t *testing.T) {
	is := is.New(t)
	s := New(mustAllow(t, "api.openai.com:443"), log.Nop())
	_, err := s.Do(context.Background(), pprocutils.HTTPRequest{
		Method: "GET", URL: "http://169.254.169.254/latest/meta-data/",
	})
	// Stage 1 rejects (not allowlisted) before we even dial.
	is.True(cerrors.Is(err, pprocutils.ErrHTTPForbidden))
}
