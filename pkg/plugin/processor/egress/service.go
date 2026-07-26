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
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// reservedHeaders are host-controlled: a guest attempt to set any of them is
// rejected with ErrHTTPInvalidRequest. Keys are canonical MIME form.
//
//   - Host / :authority: derived from the validated URL, so the guest cannot show
//     one hostname to the allowlist and another to the server (Host/SNI confusion).
//   - Authorization: host-injected from a secret ref; there is no guest-supplied
//     credential path at all.
//   - Accept-Encoding: host-controlled so Go's transparent gzip decompression
//     stays on and the response-size cap measures the DECOMPRESSED stream (a
//     guest-set Accept-Encoding would let a decompression bomb pass the cap).
//   - Connection-control headers: never guest-settable.
var reservedHeaders = map[string]struct{}{
	"Host":                {},
	":authority":          {}, // not canonicalizable, checked verbatim too
	"Authorization":       {},
	"Accept-Encoding":     {},
	"Connection":          {},
	"Proxy-Connection":    {},
	"Proxy-Authorization": {},
	"Transfer-Encoding":   {},
	"Content-Length":      {},
	"Upgrade":             {},
	"Keep-Alive":          {},
	"Te":                  {},
	"Trailer":             {},
}

// SecretResolver resolves a named secret to the value the host sets as the
// Authorization header. The concrete backend is out of scope for this slice
// (Non-goals); the interface reserves the mechanism so no guest-supplied
// credential path ever exists. A nil resolver means the secret store is not
// wired: any AuthSecretRef use fails with a clear, non-silent error.
type SecretResolver interface {
	// Resolve returns the Authorization header value for the named secret.
	Resolve(ctx context.Context, name string) (string, error)
}

// dialRefusedError is returned by the dial-time gate when a resolved IP is
// refused. It carries the audit reason so Do can map it to ErrHTTPForbidden and
// log the security-relevant denial.
type dialRefusedError struct {
	ip     net.IP
	port   string
	reason refusedReason
}

func (e *dialRefusedError) Error() string {
	return "egress: dial to " + net.JoinHostPort(e.ip.String(), e.port) + " refused (" + string(e.reason) + ")"
}

// errRedirectBlocked is returned by CheckRedirect so a 3xx never becomes an
// automatic hop out of the allowlist.
var errRedirectBlocked = cerrors.New("egress: redirects are not followed")

// dnsError marks a name-resolution failure so Do maps it to ErrHTTPDNS
// (retryable-transient), distinct from a forbidden/allowlist error (permanent).
type dnsError struct{ err error }

func (e *dnsError) Error() string { return "egress: DNS resolution failed: " + e.err.Error() }
func (e *dnsError) Unwrap() error { return e.err }

// Resolver resolves a hostname to candidate IPs. Injectable so the DNS-rebinding
// tests can return a public IP on one lookup and a private one on the next,
// proving the Stage-2 gate runs at dial time against the actual address.
type Resolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

type netResolver struct{ r *net.Resolver }

func (n netResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := n.r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	return ips, nil
}

// Service is the per-processor host-side egress implementation. It satisfies
// pprocutils.HTTPService. One Service is bound to exactly one processor
// instance's Policy (captured here and in the dialer Control closure); Services
// are never shared, so policies never leak between processors.
type Service struct {
	policy   Policy
	logger   log.CtxLogger
	resolver Resolver
	secrets  SecretResolver
	client   *http.Client
}

// Option configures a Service (test seams for resolver / secret backend).
type Option func(*Service)

// WithResolver overrides the DNS resolver (used by the rebinding tests).
func WithResolver(r Resolver) Option { return func(s *Service) { s.resolver = r } }

// WithSecretResolver wires the credential backend for AuthSecretRef.
func WithSecretResolver(sr SecretResolver) Option { return func(s *Service) { s.secrets = sr } }

// New builds a Service for one processor instance's resolved Policy.
//
// Security-critical transport configuration (design § Hardening):
//   - Transport.Proxy is pinned nil so HTTP(S)_PROXY / ALL_PROXY cannot redirect
//     the dial to a proxy the gate never validated (the complete-bypass the
//     security review found).
//   - DialContext runs the resolved-IP gate per candidate and dials the chosen
//     IP through a base dialer whose Control hook re-applies the gate at the
//     syscall level. No custom DialTLSContext exists, so the same gated dial
//     serves https as well as http (Control governs TLS dials too).
//   - CheckRedirect refuses every redirect.
//   - Transparent decompression is left ON (DisableCompression false) so the
//     size cap in Do measures the decompressed stream.
func New(policy Policy, logger log.CtxLogger, opts ...Option) *Service {
	s := &Service{
		policy:   policy,
		logger:   logger.WithComponent("egress.Service"),
		resolver: netResolver{r: net.DefaultResolver},
	}
	for _, o := range opts {
		o(s)
	}

	base := &net.Dialer{
		Timeout:   effectiveTimeout(policy),
		KeepAlive: 30 * time.Second,
		Control:   s.dialControl, // Invariant (Stage 2): syscall-level resolved-IP gate.
	}

	transport := &http.Transport{
		Proxy:                 nil, // MUST-FIX 1: never ProxyFromEnvironment.
		DialContext:           s.dialContext(base),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   effectiveTimeout(policy),
		ExpectContinueTimeout: time.Second,
		// DisableCompression stays false: transparent gzip decompression must be
		// on so the io.LimitReader in Do caps the DECOMPRESSED stream.
	}

	s.client = &http.Client{
		Transport: transport,
		Timeout:   effectiveTimeout(policy),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectBlocked
		},
	}
	return s
}

func effectiveTimeout(p Policy) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultTimeout
}

func (s *Service) maxResponseBytes() int64 {
	if s.policy.MaxResponseBytes > 0 {
		return s.policy.MaxResponseBytes
	}
	return DefaultMaxResponseBytes
}

// dialControl is the net.Dialer.Control hook — Stage 2 at the syscall level. It
// fires immediately before connect(2) for every candidate address, for http and
// TLS dials alike, and refuses unless the resolved IP is allowed by range or the
// exact (IP, port) pair is an explicit carve-out.
func (s *Service) dialControl(_, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return &dialRefusedError{reason: reasonUnparseable, port: port}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return &dialRefusedError{reason: reasonUnparseable, port: port}
	}
	if refused, reason := Refuse(ip); refused {
		if s.policy.matchesCarveOut(ip, port) {
			return nil // explicit (IP, port) carve-out (e.g. local Ollama).
		}
		return &dialRefusedError{ip: ip, port: port, reason: reason}
	}
	return nil
}

// dialContext resolves the host, applies the Stage-2 gate per candidate, and
// dials the first admissible candidate through the base dialer (whose Control
// re-checks). Doing resolution here (a) makes the gate testable with an injected
// resolver, (b) classifies a resolution failure as ErrHTTPDNS, and (c) ensures
// every candidate of a multi-answer / dual-stack result is checked, so a
// public+private mixed answer cannot smuggle a private dial through.
func (s *Service) dialContext(base *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, &dialRefusedError{reason: reasonUnparseable}
		}

		var candidates []net.IP
		if ip := net.ParseIP(host); ip != nil {
			candidates = []net.IP{ip}
		} else {
			ips, rerr := s.resolver.LookupIP(ctx, host)
			if rerr != nil {
				return nil, &dnsError{err: rerr}
			}
			if len(ips) == 0 {
				return nil, &dnsError{err: cerrors.Errorf("no addresses for %q", host)}
			}
			candidates = ips
		}

		var lastRefusal error
		for _, ip := range candidates {
			if refused, reason := Refuse(ip); refused && !s.policy.matchesCarveOut(ip, port) {
				lastRefusal = &dialRefusedError{ip: ip, port: port, reason: reason}
				continue // try the next candidate (public may follow private)
			}
			conn, derr := base.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if derr != nil {
				lastRefusal = derr
				continue
			}
			return conn, nil
		}
		if lastRefusal == nil {
			lastRefusal = &dialRefusedError{reason: reasonUnparseable, port: port}
		}
		return nil, lastRefusal
	}
}

// Do performs one host-mediated egress call under the processor's policy. It is
// the pprocutils.HTTPService implementation the host module invokes.
func (s *Service) Do(ctx context.Context, req pprocutils.HTTPRequest) (pprocutils.HTTPResponse, error) {
	start := time.Now()

	// Deny-all default: a non-opted-in processor never gets egress.
	if !s.policy.Enabled {
		return pprocutils.HTTPResponse{}, egressErr(pprocutils.ErrHTTPEgressDisabled,
			"egress is not enabled for this processor; add an allowlist entry")
	}

	u, method, err := s.validateRequestLine(req)
	if err != nil {
		return pprocutils.HTTPResponse{}, err
	}

	scheme := u.Scheme
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if scheme == schemeHTTPS {
			port = "443"
		} else {
			port = "80"
		}
	}

	// Stage 1: coarse scheme + host:port pre-filter.
	if !s.policy.MatchHostPort(scheme, host, port) {
		s.logDeny(ctx, method, host, "allowlist_miss")
		return pprocutils.HTTPResponse{}, egressErr(pprocutils.ErrHTTPForbidden,
			"destination not in the processor's egress allowlist")
	}

	httpReq, err := s.buildHTTPRequest(ctx, u, method, req)
	if err != nil {
		return pprocutils.HTTPResponse{}, err
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return pprocutils.HTTPResponse{}, s.classifyDoError(ctx, method, host, err)
	}
	defer resp.Body.Close()

	// Response-size cap on the DECOMPRESSED stream (Accept-Encoding is
	// host-reserved, so the transport has already inflated any gzip body).
	limit := s.maxResponseBytes()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(body)) > limit {
		s.logDeny(ctx, method, host, "size_cap")
		return pprocutils.HTTPResponse{}, egressErr(pprocutils.ErrHTTPResponseTooLarge,
			"response body exceeded the host size cap")
	}
	if readErr != nil {
		// A body read that trips the per-call deadline is a timeout, not a
		// generic transport error.
		if isTimeout(readErr) {
			return pprocutils.HTTPResponse{}, egressErr(pprocutils.ErrHTTPTimeout, "egress call timed out reading response body")
		}
		return pprocutils.HTTPResponse{}, egressErr(pprocutils.ErrHTTPTransport, "failed reading response body")
	}

	s.logAllow(ctx, method, host, resp.StatusCode, len(body), time.Since(start))

	return pprocutils.HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    filterResponseHeaders(resp.Header),
		Body:       body,
	}, nil
}

// validateRequestLine parses and validates the method + URL. It rejects
// non-http(s) schemes and unparseable URLs with ErrHTTPInvalidRequest.
func (s *Service) validateRequestLine(req pprocutils.HTTPRequest) (*url.URL, string, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !validToken(method) {
		return nil, "", egressErr(pprocutils.ErrHTTPInvalidRequest, "invalid HTTP method")
	}

	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, "", egressErr(pprocutils.ErrHTTPInvalidRequest, "unparseable request URL")
	}
	if u.Scheme != schemeHTTPS && u.Scheme != schemeHTTP {
		return nil, "", egressErr(pprocutils.ErrHTTPInvalidRequest, "unsupported URL scheme: "+u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, "", egressErr(pprocutils.ErrHTTPInvalidRequest, "request URL has no host")
	}
	return u, method, nil
}

// buildHTTPRequest constructs the *http.Request, enforcing the reserved-header
// denylist, validating header names/values (CRLF/control rejection), and
// injecting the host-resolved Authorization from AuthSecretRef.
func (s *Service) buildHTTPRequest(ctx context.Context, u *url.URL, method string, req pprocutils.HTTPRequest) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(""))
	if err != nil {
		return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "could not build request")
	}
	if len(req.Body) > 0 {
		httpReq.Body = io.NopCloser(strings.NewReader(string(req.Body)))
		httpReq.ContentLength = int64(len(req.Body))
	}

	for name, values := range req.Headers {
		if !validToken(name) {
			return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "invalid header name")
		}
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if _, reserved := reservedHeaders[canonical]; reserved {
			return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "header is host-reserved and cannot be set by the guest: "+canonical)
		}
		if strings.EqualFold(name, ":authority") || strings.HasPrefix(name, ":") {
			return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "pseudo-headers cannot be set by the guest")
		}
		for _, v := range values {
			if !validHeaderValue(v) {
				return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "invalid header value (control characters not allowed)")
			}
			httpReq.Header.Add(canonical, v)
		}
	}

	// Host is derived from the validated URL — never guest-controlled.
	httpReq.Host = u.Host

	// Host-injected credentials: the guest names a secret; the host sets
	// Authorization. There is no guest-supplied-credential path.
	if req.AuthSecretRef != "" {
		if _, granted := s.policy.SecretRefs[req.AuthSecretRef]; !granted {
			return nil, egressErr(pprocutils.ErrHTTPForbidden, "secret reference not granted to this processor: "+req.AuthSecretRef)
		}
		if s.secrets == nil {
			// Mechanism reserved; backend wiring deferred to a later slice.
			return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "secret store is not wired on this instance; AuthSecretRef is not yet supported")
		}
		val, serr := s.secrets.Resolve(ctx, req.AuthSecretRef)
		if serr != nil {
			return nil, egressErr(pprocutils.ErrHTTPInvalidRequest, "failed to resolve secret reference")
		}
		httpReq.Header.Set("Authorization", val)
	}

	return httpReq, nil
}

func (s *Service) classifyDoError(ctx context.Context, method, host string, err error) error {
	var refused *dialRefusedError
	if cerrors.As(err, &refused) {
		s.logDeny(ctx, method, host, "ip_refused_"+string(refused.reason))
		return egressErr(pprocutils.ErrHTTPForbidden, "resolved IP refused by egress policy")
	}
	var dnsE *dnsError
	if cerrors.As(err, &dnsE) {
		s.logDeny(ctx, method, host, "dns_failure")
		return egressErr(pprocutils.ErrHTTPDNS, "DNS resolution failed")
	}
	if cerrors.Is(err, errRedirectBlocked) {
		s.logDeny(ctx, method, host, "redirect_blocked")
		return egressErr(pprocutils.ErrHTTPForbidden, "redirects are not followed")
	}
	if isTimeout(err) {
		s.logDeny(ctx, method, host, "timeout")
		return egressErr(pprocutils.ErrHTTPTimeout, "egress call timed out")
	}
	s.logDeny(ctx, method, host, "transport_error")
	return egressErr(pprocutils.ErrHTTPTransport, "transport error")
}

func isTimeout(err error) bool {
	var nerr net.Error
	if cerrors.As(err, &nerr) {
		return nerr.Timeout()
	}
	return cerrors.Is(err, context.DeadlineExceeded)
}

// egressErr wraps a stable pprocutils ABI error code with a human-readable,
// non-secret message. host_module maps the wrapped *pprocutils.Error to the
// numeric code the guest receives.
func egressErr(code *pprocutils.Error, msg string) error {
	return cerrors.Errorf("%s: %w", msg, code)
}

// --- header helpers -------------------------------------------------------

// validToken reports whether s is a valid HTTP token (method/header name):
// no control chars, no separators, no CR/LF (header-injection defense).
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f {
			return false
		}
		switch c {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}

// validHeaderValue rejects CR, LF, and NUL (header/response splitting). Other
// bytes are permitted (values are less restricted than names).
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\r', '\n', 0:
			return false
		}
	}
	return true
}

// filterResponseHeaders copies response headers into a plain map, dropping
// hop-by-hop headers. Authorization is a request header and never echoed here.
func filterResponseHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// --- audit logging (redaction mandatory: never log bodies or Authorization) --

func (s *Service) logAllow(ctx context.Context, method, host string, status, respBytes int, latency time.Duration) {
	s.logger.Info(ctx).
		Str("decision", "allow").
		Str("method", method).
		Str("request_host", host).
		Int("status", status).
		Int("response_bytes", respBytes).
		Dur("latency", latency).
		Msg("egress call")
}

func (s *Service) logDeny(ctx context.Context, method, host, reason string) {
	// Deny events are the audit trail for an attempted SSRF; log at Warn so they
	// survive normal filtering.
	s.logger.Warn(ctx).
		Str("decision", "deny").
		Str("method", method).
		Str("request_host", host).
		Str("reason", reason).
		Msg("egress call denied")
}
