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

package connegress

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// This file is the dial-time half of the policy. A decision made on the text of
// a setting when a connector is configured can disagree with what the client
// library really dials (a different endpoint resolution, a name that resolves
// differently later, redirects, fallbacks), so the authoritative place to
// enforce the policy is the dial itself: resolve the name here, vet every
// address, and connect to the address that was vetted. [Policy.DialContext] does
// that and the adapters below fit it to the dial hooks of the client libraries
// the built-in connectors use (pgx, franz-go, the AWS SDK's HTTP client, a MySQL
// driver).
//
// The built-in connectors do not take a dial hook from the engine yet: it needs
// a seam in conduit-commons next to hostpolicy and a release of each connector.
// Until then the configure-time check is the only enforcement in a running
// engine; see docs/operations/connector-egress.md.

// DialContext connects to addr like net.Dialer.DialContext, after vetting it.
// It never trusts an earlier [Policy.Hint]: the host is resolved here, every
// address must pass the policy (metadata addresses always fail; with an
// allowlist the name or all of its addresses must be listed), and the connection
// is made to a vetted address, not to the name, so a second DNS answer cannot
// redirect it. A target that cannot be vetted (no host, an unresolvable name, a
// unix socket under an allowlist) is refused.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	p = p.orZero()
	d := &net.Dialer{Timeout: 30 * time.Second}
	if strings.HasPrefix(network, "unix") {
		if p.Restricted() {
			return nil, refuse("dial", Target{Host: addr, Port: "0"}, "unix sockets are not allowed while connectors.egress.allow is set")
		}
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, p.CheckUnvettable("dial", "the address "+addr+" has no host and port")
	}
	t := Target{Host: host, Port: port}
	nh := normalizeHost(host)
	if done, err := p.checkLiteral("dial", t, nh); done {
		if err != nil {
			return nil, err
		}
		return d.DialContext(ctx, network, net.JoinHostPort(strings.Trim(host, "[]"), port))
	}

	rctx, cancel := context.WithTimeout(ctx, p.timeout)
	addrs, err := p.resolver.LookupIPAddr(rctx, nh)
	cancel()
	if err != nil || len(addrs) == 0 {
		return nil, refuse("dial", t, "the host name cannot be resolved, so it cannot be vetted")
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	if err := p.vetIPs("dial", t, !p.Restricted() || p.allowsName(nh, port), ips); err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// PGXDialFunc is [Policy.DialContext] as a pgconn.DialFunc
// (pgconn.Config.DialFunc). pgx calls it for every host and fallback.
func (p *Policy) PGXDialFunc() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.DialContext
}

// KafkaDialer is [Policy.DialContext] as a franz-go dialer (kgo.Dialer).
func (p *Policy) KafkaDialer() func(ctx context.Context, network, host string) (net.Conn, error) {
	return p.DialContext
}

// MySQLDialContext is [Policy.DialContext] as a go-sql-driver/mysql dial
// function (mysql.RegisterDialContext) for the given network ("tcp").
func (p *Policy) MySQLDialContext(network string) func(ctx context.Context, addr string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) { return p.DialContext(ctx, network, addr) }
}

// HTTPTransport returns an http.Transport whose every connection goes through
// [Policy.DialContext], for the AWS SDK's HTTP client (an S3 endpoint chosen by
// the SDK is checked as dialed, whatever it is). It ignores proxy variables: a
// proxy hop cannot be vetted. TLS verification is left at the defaults.
func (p *Policy) HTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // the standard library's default
	t.Proxy = nil
	t.DialContext = p.DialContext
	return t
}

type policyKey struct{}

// WithPolicy returns a context that carries p. The engine puts it on the call
// context of the built-in connectors that connect somewhere (postgres, kafka,
// s3), the way it hands the file connector its opener, so a connector that
// takes a dial hook can build its client from [PolicyFrom](ctx).DialContext.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// PolicyFrom returns the policy on ctx, or nil.
func PolicyFrom(ctx context.Context) *Policy {
	p, _ := ctx.Value(policyKey{}).(*Policy)
	return p
}
