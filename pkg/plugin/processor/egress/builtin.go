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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeEgressBlocked is raised when a built-in processor (webhook.http,
// ollama.request, openai.*, cohere.*) is about to connect to an address that
// the dial-time guard refuses: a private, loopback, link-local, metadata,
// reserved or embedded-IPv4 address, whether given as an IP literal (in any
// encoding), reached through a DNS name, or reached through a redirect. The fix
// is for an operator to allow the specific host:port in
// processors.egress.builtin-allow and restart; it cannot be changed through
// the API.
var CodeEgressBlocked = conduiterr.Register("processor.egress_blocked", codes.PermissionDenied)

// BuiltinAllowConfigPath is the operator config key that exempts specific
// destinations from the dial-time guard for built-in processors.
const BuiltinAllowConfigPath = "processors.egress.builtin-allow"

// maxRedirects bounds redirect chains followed by a guarded client.
const maxRedirects = 10

// DialGuard builds HTTP clients for built-in processors that enforce the same
// resolved-IP policy as the WASM host egress (see [Refuse]) at dial time: for
// every candidate address of every connection, including each redirect hop,
// unless the exact destination is in the operator's allow list.
//
// Unlike the WASM [Service], built-in processors keep reaching the public
// internet by default (their URLs are the pipeline author's), so there is no
// host allowlist here: the guard only closes the private, loopback,
// link-local and metadata ranges. The only exemptions are the operator's
// builtin-allow entries, matched on (IP, port) or (hostname, port).
//
// Hardening, all unconditional: Transport.Proxy is nil so HTTP(S)_PROXY /
// ALL_PROXY cannot route around the guard; redirects are followed at most
// maxRedirects times, never from https to http, and every hop is dialed
// through the guard.
//
// A nil *DialGuard is valid and allows no exemption.
type DialGuard struct {
	allow    []AllowEntry
	resolver Resolver
}

// NewDialGuard returns a guard with the given operator exemptions.
func NewDialGuard(allow []AllowEntry) *DialGuard {
	return &DialGuard{allow: append([]AllowEntry(nil), allow...), resolver: netResolver{r: net.DefaultResolver}}
}

// WithResolver returns a copy of the guard that resolves names through r. It
// exists for DNS-rebinding tests.
func (g *DialGuard) WithResolver(r Resolver) *DialGuard {
	c := *g.orEmpty()
	c.resolver = r
	return &c
}

func (g *DialGuard) orEmpty() *DialGuard {
	if g == nil {
		return NewDialGuard(nil)
	}
	return g
}

// exempt reports whether the operator allowed this destination.
func (g *DialGuard) exempt(host string, ip net.IP, port string) bool {
	if g == nil {
		return false
	}
	host = strings.ToLower(host)
	for _, e := range g.allow {
		if e.Port != port {
			continue
		}
		if e.IsIP() {
			if e.IP.Equal(ip) {
				return true
			}
			continue
		}
		if e.Host == host {
			return true
		}
	}
	return false
}

// HTTPClient returns a client whose every connection passes the guard. timeout
// bounds a whole request; zero means DefaultTimeout.
func (g *DialGuard) HTTPClient(timeout time.Duration) *http.Client {
	g = g.orEmpty()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{
		Transport: g.Transport(timeout),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return blockedErr("redirect", "", "too many redirects")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == schemeHTTPS && req.URL.Scheme != schemeHTTPS {
				return blockedErr("redirect", req.URL.Hostname(), "redirect from https to http")
			}
			return nil // the next hop is dialed through the guard like any connection
		},
	}
}

// Transport returns the guarded transport. Callers that need their own
// http.Client (SDKs that take one) should prefer HTTPClient.
func (g *DialGuard) Transport(timeout time.Duration) *http.Transport {
	g = g.orEmpty()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Transport{
		Proxy:                 nil, // never ProxyFromEnvironment: a proxy hop would not be validated
		DialContext:           g.dialContext(timeout),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ExpectContinueTimeout: time.Second,
	}
}

func (g *DialGuard) dialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, blockedErr("dial", address, "unparseable address")
		}

		var candidates []net.IP
		if ip := net.ParseIP(host); ip != nil {
			candidates = []net.IP{ip}
		} else {
			ips, rerr := g.resolver.LookupIP(ctx, host)
			if rerr != nil {
				return nil, &dnsError{err: rerr}
			}
			if len(ips) == 0 {
				return nil, &dnsError{err: cerrors.Errorf("no addresses for %q", host)}
			}
			candidates = ips
		}

		// Every candidate is classified, so a mixed public/private answer
		// cannot smuggle a private dial through. The first admissible one is
		// dialed by IP, so the address checked is the address connected to
		// (no second lookup, no rebinding window).
		var lastErr error
		reason := reasonUnparseable
		for _, ip := range candidates {
			if refused, r := refuseIP(ip); refused && !g.exempt(host, ip, port) {
				reason = r
				continue
			}
			approved := net.JoinHostPort(ip.String(), port)
			d := &net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
				// Defense in depth at the syscall: connect(2) only to the
				// exact address approved above, or to one the range check
				// accepts on its own.
				Control: func(_, addr string, _ syscall.RawConn) error {
					if addr == approved {
						return nil
					}
					cip, _, _ := net.SplitHostPort(addr)
					if refused, r := refuseIP(net.ParseIP(cip)); refused {
						return blockedErr("dial", host, string(r))
					}
					return nil
				},
			}
			conn, derr := d.DialContext(ctx, network, approved)
			if derr != nil {
				lastErr = derr
				continue
			}
			return conn, nil
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, blockedErr("dial", net.JoinHostPort(host, port), string(reason))
	}
}

// blockedErr builds the coded refusal. It never includes the resolved IP: for
// an internal DNS name that would describe the network to the caller.
func blockedErr(what, target, why string) error {
	msg := "outbound request refused by the egress guard (" + what
	if target != "" {
		msg += " to " + target
	}
	msg += "): " + why
	ce := conduiterr.New(CodeEgressBlocked, msg)
	ce.Suggestion = "the destination resolves to a private, loopback, link-local or metadata address; use a public address, or have an operator add the exact host:port to " +
		BuiltinAllowConfigPath + " in conduit.yaml (or pass --" + BuiltinAllowConfigPath + ") and restart; this cannot be changed through the API (see docs/operations/outbound-http-egress.md)"
	return ce
}

// ParseBuiltinAllow parses the operator's builtin-allow entries with the same
// grammar as processors.egress.allow (host[:port] or scheme://host[:port]; no
// wildcards, no paths; http only for an IP literal). A malformed entry is an
// error, never silently dropped.
func ParseBuiltinAllow(entries []string) ([]AllowEntry, error) {
	return ParseAllowlist(strings.Join(entries, ","))
}

// GuardHolder is embedded by built-in processors that open outbound
// connections. It lets the built-in registry hand each processor the
// operator's DialGuard (the registry looks for a SetDialGuard method). A
// processor that never receives one uses the default guard, which blocks the
// private ranges with no exemption.
type GuardHolder struct {
	mu     sync.Mutex
	guard  *DialGuard
	client *http.Client
}

// SetDialGuard sets the guard used by the processor's HTTP clients. It is
// called once, before the processor processes records.
func (h *GuardHolder) SetDialGuard(g *DialGuard) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.guard = g
	h.client = nil
}

// Guard returns the guard, or nil (which is valid and exempts nothing).
func (h *GuardHolder) Guard() *DialGuard {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.guard
}

// HTTPClient returns the processor's guarded client, built once so connections
// are reused.
func (h *GuardHolder) HTTPClient() *http.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		h.client = h.guard.HTTPClient(0)
	}
	return h.client
}

// refuseIP is Refuse with plain IPv4 addresses normalized to their 4-byte form,
// so a refusal of 127.0.0.1 is labeled loopback rather than v4-mapped (net's
// ParseIP returns the 16-byte form for every IPv4 literal). The decision is
// the same either way.
func refuseIP(ip net.IP) (bool, refusedReason) {
	if v4 := ip.To4(); v4 != nil {
		return Refuse(v4)
	}
	return Refuse(ip)
}
