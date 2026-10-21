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
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// counter starts a loopback HTTP server that counts requests.
func counter(t *testing.T, h http.Handler) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if h != nil {
			h.ServeHTTP(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func get(g *DialGuard, rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := g.HTTPClient(2 * time.Second).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func requireBlocked(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("request succeeded, want it refused")
	}
	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != CodeEgressBlocked {
		t.Fatalf("error %v is not coded %s", err, CodeEgressBlocked.Reason())
	}
	if ce.Suggestion == "" || !strings.Contains(ce.Suggestion, BuiltinAllowConfigPath) {
		t.Fatalf("suggestion %q does not name the operator key", ce.Suggestion)
	}
}

// fixedResolver answers every name with ips.
type fixedResolver []net.IP

func (f fixedResolver) LookupIP(context.Context, string) ([]net.IP, error) { return f, nil }

func TestGuard_BlocksPrivateLiterals(t *testing.T) {
	srv, hits := counter(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, host := range []string{
		"127.0.0.1",
		"[::1]",
		"[::ffff:127.0.0.1]", // IPv4-mapped
		"[::ffff:7f00:1]",    // IPv4-mapped, hex form
		"[::127.0.0.1]",      // IPv4-compatible
		"[64:ff9b::7f00:1]",  // NAT64
		"[2002:7f00:1::]",    // 6to4
		"169.254.169.254",    // AWS/GCP/Azure metadata
		"[fd00:ec2::254]",    // AWS IPv6 metadata
		"100.100.100.200",    // Alibaba metadata (CGNAT)
		"168.63.129.16",      // Azure WireServer
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"0.0.0.0",
	} {
		t.Run(host, func(t *testing.T) {
			requireBlocked(t, get(nil, "http://"+host+":"+port+"/"))
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback server was reached %d times", hits.Load())
	}
}

// Decimal, octal and hex IPv4 spellings are not IP literals to net.ParseIP, so
// they reach the resolver as names. Some system resolvers (getaddrinfo) map them
// to the loopback address; the guard must refuse that answer. The resolver here
// plays that worst case deterministically.
func TestGuard_NumericHostSpellings(t *testing.T) {
	srv, hits := counter(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, host := range []string{
		"2130706433",   // decimal
		"0x7f000001",   // hex
		"017700000001", // octal
		"0177.0.0.1",   // dotted octal
		"0x7f.0.0.1",   // dotted hex
		"127.1",        // short form
		"127.0.1",
		"0",
	} {
		t.Run(host, func(t *testing.T) {
			g := NewDialGuard(nil).WithResolver(fixedResolver{net.ParseIP("127.0.0.1")})
			requireBlocked(t, get(g, "http://"+host+":"+port+"/"))
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback server was reached %d times", hits.Load())
	}
}

func TestGuard_DNSRebindingAndMixedAnswers(t *testing.T) {
	srv, hits := counter(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for name, ips := range map[string][]net.IP{
		"name resolves to loopback":       {net.ParseIP("127.0.0.1")},
		"name resolves to metadata":       {net.ParseIP("169.254.169.254")},
		"name resolves to private v6":     {net.ParseIP("fd00::1")},
		"private first, then loopback":    {net.ParseIP("10.1.2.3"), net.ParseIP("127.0.0.1")},
		"mapped loopback":                 {net.ParseIP("::ffff:127.0.0.1")},
		"nat64-synthesized metadata":      {net.ParseIP("64:ff9b::a9fe:a9fe")},
		"ipv4-translated synthesized one": {net.ParseIP("::ffff:0:7f00:1")},
	} {
		t.Run(name, func(t *testing.T) {
			g := NewDialGuard(nil).WithResolver(fixedResolver(ips))
			requireBlocked(t, get(g, "http://rebind.example:"+port+"/"))
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback server was reached %d times", hits.Load())
	}
}

// The address that was classified is the address that is dialed: a resolver that
// would answer differently the second time is irrelevant, because there is no
// second lookup.
func TestGuard_DialsTheAddressItChecked(t *testing.T) {
	srv, hits := counter(t, nil)
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)

	var calls atomic.Int64
	flip := resolverFunc(func(context.Context, string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	})
	e, err := ParseAllowEntry("http://127.0.0.1:" + port)
	if err != nil {
		t.Fatal(err)
	}
	g := NewDialGuard([]AllowEntry{e}).WithResolver(flip)
	if err := get(g, "http://flip.example:"+port+"/"); err != nil {
		t.Fatalf("exempt destination refused: %v", err)
	}
	if hits.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("hits=%d lookups=%d, want 1 and 1", hits.Load(), calls.Load())
	}
}

type resolverFunc func(context.Context, string) ([]net.IP, error)

func (f resolverFunc) LookupIP(ctx context.Context, h string) ([]net.IP, error) { return f(ctx, h) }

func TestGuard_RedirectsToBlockedTargets(t *testing.T) {
	target, targetHits := counter(t, nil)
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	redirectTo := func(loc string) *httptest.Server {
		s, _ := counter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, loc, http.StatusFound)
		}))
		return s
	}
	exemptFor := func(s *httptest.Server) *DialGuard {
		e, err := ParseAllowEntry("http://" + s.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return NewDialGuard([]AllowEntry{e})
	}

	for name, loc := range map[string]string{
		"redirect to metadata":             "http://169.254.169.254/latest/meta-data/",
		"redirect to non-exempt loopback":  "http://127.0.0.1:" + targetPort + "/",
		"redirect to ipv6 mapped loopback": "http://[::ffff:127.0.0.1]:" + targetPort + "/",
		"redirect to decimal loopback":     "http://2130706433:" + targetPort + "/",
		"redirect to localhost name":       "http://localhost:" + targetPort + "/",
		"redirect to private range":        "http://10.0.0.5/",
	} {
		t.Run(name, func(t *testing.T) {
			s := redirectTo(loc)
			err := get(exemptFor(s), s.URL)
			if err == nil {
				t.Fatal("redirect to a blocked target was followed")
			}
			var ue *url.Error
			if !cerrors.As(err, &ue) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
		})
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was reached %d times", targetHits.Load())
	}

	t.Run("redirect to metadata is a coded refusal", func(t *testing.T) {
		s := redirectTo("http://169.254.169.254/latest/meta-data/")
		requireBlocked(t, get(exemptFor(s), s.URL))
	})
}

func TestGuard_RedirectLimit(t *testing.T) {
	var srv *httptest.Server
	srv, _ = counter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	e, _ := ParseAllowEntry("http://" + srv.Listener.Addr().String())
	if err := get(NewDialGuard([]AllowEntry{e}), srv.URL); err == nil {
		t.Fatal("redirect loop was followed forever")
	}
}

// HTTP(S)_PROXY / ALL_PROXY must not redirect the dial to a proxy the guard
// never validated.
func TestGuard_IgnoresProxyEnvironment(t *testing.T) {
	proxy, proxyHits := counter(t, nil)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	srv, hits := counter(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	e, _ := ParseAllowEntry("http://127.0.0.1:" + port)
	g := NewDialGuard([]AllowEntry{e}).WithResolver(fixedResolver{net.ParseIP("127.0.0.1")})

	// A non-loopback host name: net/http's ProxyFromEnvironment would send this
	// to the proxy. The guard dials the resolved address itself.
	if err := get(g, "http://app.internal.example:"+port+"/"); err != nil {
		t.Fatal(err)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("request went through the environment proxy (%d hits)", proxyHits.Load())
	}
	if hits.Load() != 1 {
		t.Fatalf("destination hits = %d, want 1", hits.Load())
	}
	if tr, ok := g.HTTPClient(0).Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatal("transport has a Proxy function")
	}
}

func TestGuard_ExemptionIsExact(t *testing.T) {
	a, aHits := counter(t, nil)
	b, bHits := counter(t, nil)
	e, _ := ParseAllowEntry("http://" + a.Listener.Addr().String())
	g := NewDialGuard([]AllowEntry{e})

	if err := get(g, a.URL); err != nil {
		t.Fatalf("exempt destination refused: %v", err)
	}
	// same IP, different port: not exempt
	requireBlocked(t, get(g, b.URL))
	if aHits.Load() != 1 || bHits.Load() != 0 {
		t.Fatalf("hits a=%d b=%d, want 1 and 0", aHits.Load(), bHits.Load())
	}

	// a nil guard exempts nothing
	var nilGuard *DialGuard
	requireBlocked(t, get(nilGuard, a.URL))
}

func TestGuard_HostnameExemption(t *testing.T) {
	srv, hits := counter(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	e := AllowEntry{Scheme: "https", Host: "ollama.internal", Port: port}
	g := NewDialGuard([]AllowEntry{e}).WithResolver(fixedResolver{net.ParseIP("127.0.0.1")})

	if err := get(g, "http://ollama.internal:"+port+"/"); err != nil {
		t.Fatalf("exempt hostname refused: %v", err)
	}
	// a different name that resolves to the same address is not exempt
	requireBlocked(t, get(g, "http://other.internal:"+port+"/"))
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestParseBuiltinAllow(t *testing.T) {
	got, err := ParseBuiltinAllow([]string{"http://127.0.0.1:11434", "ollama.internal:11434"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"*.internal", "http://ollama.internal", "http://127.0.0.1:1/path", "ftp://x"} {
		if _, err := ParseBuiltinAllow([]string{bad}); err == nil {
			t.Errorf("entry %q accepted", bad)
		}
	}
}

func TestRefuse_AdditionalSpecialRanges(t *testing.T) {
	for _, ip := range []string{"168.63.129.16", "192.0.0.8", "198.18.0.1", "198.19.255.255"} {
		if refused, _ := Refuse(net.ParseIP(ip)); !refused {
			t.Errorf("%s not refused", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "168.63.129.17"} {
		if refused, _ := Refuse(net.ParseIP(ip)); refused {
			t.Errorf("%s refused", ip)
		}
	}
}
