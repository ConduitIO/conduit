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

// Package connegress confines where built-in connectors may connect.
//
// Who may choose a connection target: pipeline settings come from the API, and
// writers of the API are authenticated and trusted to point a connector at a
// database, a Kafka cluster or an S3 endpoint. This package does not try to take
// that away. It does two things on top:
//
//   - Always, for every connector: a target that is a cloud metadata endpoint
//     (169.254.169.254 in any encoding, its link-local /16, fd00:ec2::/32,
//     metadata.google.internal, the Azure WireServer 168.63.129.16, ...) is
//     refused, whether it is written as an IP, as a host name, or is what a host
//     name resolves to. Private, loopback and other internal ranges are NOT
//     refused here: databases live there.
//   - Optionally, when the operator sets connectors.egress.allow: only the listed
//     host/CIDR:port targets may be used at all, names are resolved and every
//     address is checked, so a name cannot lead somewhere the list does not allow.
//
// What this is not: it checks the settings when the connector is configured. A
// connector that later resolves the name again gets whatever DNS answers then
// (DNS rebinding); the operator allowlist narrows that to hosts the operator
// trusts to resolve sanely. A built-in connector's own redirects and library
// behaviour are not intercepted.
package connegress

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// AllowConfigPath is the operator key that holds the allowlist.
const AllowConfigPath = "connectors.egress.allow"

// CodeEgressBlocked is raised when a connector setting names a target that is
// not allowed. It cannot be changed through the API: a metadata endpoint is
// never allowed, and the allowlist is the operator's.
var CodeEgressBlocked = conduiterr.Register("connector.egress_blocked", codes.PermissionDenied)

// Entry is one allowlist entry: a host name (optionally "*.suffix"), an IP or a
// CIDR, and a port ("*" for any).
type Entry struct {
	Host string     // lower-case host name or "*.suffix"; empty for IP and CIDR entries
	IP   net.IP     // set for an IP entry
	Net  *net.IPNet // set for a CIDR entry
	Port string     // decimal or "*"
}

// ParseEntries parses allowlist entries: "db.example.com:5432",
// "10.1.2.3:9092", "10.0.0.0/8:5432", "[fd00::/8]:5432", "*.example.com:443",
// "kafka:*".
func ParseEntries(entries []string) ([]Entry, error) {
	var out []Entry
	for _, raw := range entries {
		for _, s := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			e, err := parseEntry(s)
			if err != nil {
				return nil, cerrors.Errorf("%s entry %q: %w", AllowConfigPath, s, err)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func parseEntry(s string) (Entry, error) {
	var host, port string
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 || end+1 >= len(s) || s[end+1] != ':' {
			return Entry{}, cerrors.New("expected [address]:port")
		}
		host, port = s[1:end], s[end+2:]
	} else {
		i := strings.LastIndex(s, ":")
		if i < 0 {
			return Entry{}, cerrors.New("expected host:port")
		}
		host, port = s[:i], s[i+1:]
	}
	if host == "" {
		return Entry{}, cerrors.New("empty host")
	}
	if port != "*" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Entry{}, cerrors.Errorf("invalid port %q", port)
		}
	}
	e := Entry{Port: port}
	switch {
	case strings.Contains(host, "/"):
		_, n, err := net.ParseCIDR(host)
		if err != nil {
			return Entry{}, cerrors.Errorf("invalid CIDR: %w", err)
		}
		e.Net = n
	case net.ParseIP(host) != nil:
		e.IP = net.ParseIP(host)
	default:
		if strings.ContainsAny(host, " /@") {
			return Entry{}, cerrors.New("invalid host name")
		}
		e.Host = strings.ToLower(strings.TrimSuffix(host, "."))
	}
	return e, nil
}

func (e Entry) portMatches(port string) bool { return e.Port == "*" || e.Port == port }

func (e Entry) matchesName(host string) bool {
	switch {
	case e.Host == "":
		return false
	case strings.HasPrefix(e.Host, "*."):
		return strings.HasSuffix(host, e.Host[1:]) && len(host) > len(e.Host)-1
	}
	return e.Host == host
}

func (e Entry) matchesIP(ip net.IP) bool {
	switch {
	case e.IP != nil:
		return e.IP.Equal(ip)
	case e.Net != nil:
		return e.Net.Contains(ip)
	}
	return false
}

// Resolver resolves host names. net.DefaultResolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Policy decides whether a connector may use a target. The zero value (and nil)
// has no allowlist: only the metadata refusal applies.
type Policy struct {
	allow    []Entry
	resolver Resolver
	timeout  time.Duration
}

// New returns a policy with the operator's allowlist (nil or empty: none).
func New(allow []Entry) *Policy {
	return &Policy{allow: append([]Entry(nil), allow...), resolver: net.DefaultResolver, timeout: 3 * time.Second}
}

// WithResolver returns a copy that resolves through r (for tests).
func (p *Policy) WithResolver(r Resolver) *Policy {
	c := *p.orZero()
	c.resolver = r
	return &c
}

func (p *Policy) orZero() *Policy {
	if p == nil {
		return New(nil)
	}
	return p
}

// Restricted reports whether an allowlist is set.
func (p *Policy) Restricted() bool { return p != nil && len(p.allow) > 0 }

// Target is a place a connector will connect to.
type Target struct {
	// Host is a name or an IP literal in any encoding, or a unix socket path
	// (starting with "/").
	Host string
	// Port is a decimal port.
	Port string
}

func (t Target) String() string { return net.JoinHostPort(t.Host, t.Port) }

// Check returns nil if the connector may use t, else a coded error whose config
// path is configPath. With resolve, a host name is looked up and every address
// is held to the same rules as the name; without it (validation requests, which
// must not be a way to probe the network) only the name is checked.
func (p *Policy) Check(ctx context.Context, configPath string, t Target, resolve bool) error {
	p = p.orZero()
	host := normalizeHost(t.Host)

	if strings.HasPrefix(host, "/") || strings.HasPrefix(host, "@") {
		if p.Restricted() {
			return refuse(configPath, t, "unix sockets are not allowed while connectors.egress.allow is set")
		}
		return nil
	}
	if IsMetadataHost(host) {
		return refuse(configPath, t, "the target is a cloud metadata endpoint, which connectors may never reach")
	}
	if ip := ParseIPLoose(host); ip != nil {
		if IsMetadataIP(ip) {
			return refuse(configPath, t, "the target is a cloud metadata address, which connectors may never reach")
		}
		if p.Restricted() && !p.allowsIP(ip, t.Port) {
			return refuse(configPath, t, "the target is not in connectors.egress.allow")
		}
		return nil
	}

	return p.checkName(ctx, configPath, t, host, resolve)
}

// checkName vets a host name (not an address, not a metadata name).
func (p *Policy) checkName(ctx context.Context, configPath string, t Target, host string, resolve bool) error {
	nameAllowed := !p.Restricted() || p.allowsName(host, t.Port)
	if !resolve {
		if !nameAllowed && !p.hasAddrEntries(t.Port) {
			return refuse(configPath, t, "the target is not in connectors.egress.allow")
		}
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	addrs, err := p.resolver.LookupIPAddr(rctx, host)
	if err != nil || len(addrs) == 0 {
		// Nothing to dial now; the connector reports the failure itself. The
		// name still has to be allowed.
		if !nameAllowed {
			return refuse(configPath, t, "the target is not in connectors.egress.allow")
		}
		return nil
	}
	for _, a := range addrs {
		if IsMetadataIP(a.IP) {
			return refuse(configPath, t, "the host name resolves to a cloud metadata address, which connectors may never reach")
		}
		if p.Restricted() && !nameAllowed && !p.allowsIP(a.IP, t.Port) {
			return refuse(configPath, t, "the target is not in connectors.egress.allow and the host name resolves outside it")
		}
	}
	return nil
}

func (p *Policy) allowsIP(ip net.IP, port string) bool {
	for _, e := range p.allow {
		if e.portMatches(port) && e.matchesIP(ip) {
			return true
		}
	}
	return false
}

func (p *Policy) allowsName(host, port string) bool {
	for _, e := range p.allow {
		if e.portMatches(port) && e.matchesName(host) {
			return true
		}
	}
	return false
}

func (p *Policy) hasAddrEntries(port string) bool {
	for _, e := range p.allow {
		if e.portMatches(port) && (e.IP != nil || e.Net != nil) {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	if i := strings.Index(h, "%"); i >= 0 && strings.Contains(h, ":") {
		h = h[:i] // zone
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func refuse(configPath string, t Target, why string) error {
	ce := conduiterr.New(CodeEgressBlocked, fmt.Sprintf("connecting to %q is not allowed: %s", t.String(), why))
	ce.ConfigPath = configPath
	ce.Suggestion = "use a different target; to allow a target an operator can add host:port to " + AllowConfigPath +
		" in conduit.yaml (or pass --" + AllowConfigPath + ") and restart, which cannot be changed through the API. " +
		"Cloud metadata endpoints can never be allowed (see docs/operations/connector-egress.md)"
	return ce
}
