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
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Supported egress URL schemes. https is the default; http is permitted only
// for an explicitly-allowlisted private/loopback IP-literal target.
const (
	schemeHTTPS = "https"
	schemeHTTP  = "http"
)

const (
	// DefaultTimeout bounds dial + TLS + response-header + body read for a single
	// egress call. Matches the parent AI-pipeline design's provider timeout.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxResponseBytes caps the (decompressed) response body the host will
	// buffer. Embedding responses are small JSON; this is a DoS bound, and it must
	// stay comfortably below WASM32's 4 GiB linear-memory ceiling.
	DefaultMaxResponseBytes int64 = 4 << 20 // 4 MiB
)

// AllowEntry is one resolved allowlist entry: a (scheme, host, port) triple.
// Host is either a DNS hostname or an IP literal; an IP-literal entry is also
// the (IP, port) carve-out that admits an otherwise-refused dial in Stage 2.
type AllowEntry struct {
	Scheme string // "https" or "http"
	Host   string // lowercased hostname, or the canonical string form of IP
	Port   string // explicit port (defaulted from scheme when omitted)

	// IP is set iff the entry is an IP literal. Such an entry doubles as the
	// explicit (IP, port) Stage-2 carve-out.
	IP net.IP
}

// IsIP reports whether the entry is an IP literal (a carve-out candidate).
func (e AllowEntry) IsIP() bool { return e.IP != nil }

// Policy is the resolved, immutable per-processor egress policy. The zero value
// is deny-all (Enabled == false): a Do call returns ErrHTTPEgressDisabled.
//
// A Policy is captured by value in a single Service's dialer Control closure and
// is never shared across processor instances or held on a registry field, which
// is what keeps one pipeline's allowlist from leaking into another's egress.
type Policy struct {
	Enabled bool

	// Allowlist is the Stage-1 host:port pre-filter and the source of the
	// Stage-2 (IP, port) carve-outs (its IP-literal entries).
	Allowlist []AllowEntry

	// SecretRefs is the set of secret names this processor may reference via
	// HTTPRequest.AuthSecretRef for host-injected Authorization. A guest cannot
	// reference a secret its pipeline was not granted.
	SecretRefs map[string]struct{}

	Timeout          time.Duration
	MaxResponseBytes int64
}

// DenyAll returns an explicitly-disabled policy (deny-all). It is the policy for
// every non-data-path NewProcessor call (existence checks, plugin inspection):
// such a processor is torn down without ever processing records, so it needs no
// egress, and defaulting it to deny-all keeps the boundary closed by default.
func DenyAll() Policy { return Policy{} }

// matchesCarveOut reports whether the exact (ip, port) pair is an explicit
// IP-literal allowlist entry. The match is on the (IP, port) PAIR, never the IP
// alone: allowlisting 127.0.0.1:11434 (Ollama) must not admit 127.0.0.1:6379.
func (p Policy) matchesCarveOut(ip net.IP, port string) bool {
	for _, e := range p.Allowlist {
		if e.IsIP() && e.Port == port && e.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// MatchHostPort is Stage 1: the coarse scheme + host:port pre-filter. It reports
// whether the parsed request target matches an allowlist entry. It is NOT the
// security guarantee (a matched hostname can still resolve to a hostile IP); the
// dial-time Guard.Refuse gate in Stage 2 is.
func (p Policy) MatchHostPort(scheme, host, port string) bool {
	host = strings.ToLower(host)
	reqIP := net.ParseIP(host)
	for _, e := range p.Allowlist {
		if e.Scheme != scheme || e.Port != port {
			continue
		}
		if e.IsIP() {
			if reqIP != nil && e.IP.Equal(reqIP) {
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

// ParseAllowEntry parses a single allowlist entry. Accepted forms:
//
//	host                (scheme https, port 443)
//	host:port           (scheme https)
//	https://host[:port]
//	http://ip[:port]    (http permitted ONLY for a private/loopback IP literal)
//
// Broad wildcards are rejected (a "*." prefix is a footgun); entries are exact
// hosts. http is refused for anything but a private/loopback IP literal target.
func ParseAllowEntry(raw string) (AllowEntry, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return AllowEntry{}, cerrors.New("empty allowlist entry")
	}
	if strings.Contains(s, "*") {
		return AllowEntry{}, cerrors.Errorf("wildcard allowlist entries are not permitted: %q", raw)
	}

	scheme := schemeHTTPS
	hostPort := s
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return AllowEntry{}, cerrors.Errorf("invalid allowlist entry %q: %w", raw, err)
		}
		if u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS {
			return AllowEntry{}, cerrors.Errorf("unsupported scheme %q in allowlist entry %q", u.Scheme, raw)
		}
		scheme = u.Scheme
		hostPort = u.Host
		if u.Path != "" && u.Path != "/" {
			return AllowEntry{}, cerrors.Errorf("allowlist entry %q must not contain a path", raw)
		}
	}

	host := hostPort
	port := ""
	if h, p, err := net.SplitHostPort(hostPort); err == nil {
		host, port = h, p
	} else if strings.Contains(hostPort, "]") {
		// bracketed IPv6 without a port, e.g. "[::1]"
		host = strings.TrimSuffix(strings.TrimPrefix(hostPort, "["), "]")
	}
	if host == "" {
		return AllowEntry{}, cerrors.Errorf("allowlist entry %q has no host", raw)
	}
	if port == "" {
		if scheme == schemeHTTPS {
			port = "443"
		} else {
			port = "80"
		}
	}

	ip := net.ParseIP(host)
	if scheme == schemeHTTP {
		if ip == nil {
			return AllowEntry{}, cerrors.Errorf("http scheme in allowlist entry %q is only permitted for a private/loopback IP literal", raw)
		}
		if refused, _ := Refuse(ip); !refused {
			return AllowEntry{}, cerrors.Errorf("http scheme in allowlist entry %q is only permitted for a private/loopback target", raw)
		}
	}

	return AllowEntry{Scheme: scheme, Host: host, Port: port, IP: ip}, nil
}

// ParseAllowlist parses a comma/whitespace-separated list of entries.
func ParseAllowlist(raw string) ([]AllowEntry, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	out := make([]AllowEntry, 0, len(fields))
	for _, f := range fields {
		e, err := ParseAllowEntry(f)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// entryKey is the identity used when intersecting a per-processor allowlist with
// the engine ceiling.
func entryKey(e AllowEntry) string { return e.Scheme + "|" + e.Host + "|" + e.Port }

// intersectRefs returns the secret-ref names present in BOTH the per-processor
// request and the ceiling grant. It is the secret-scope analog of the allowlist
// intersection: a pipeline can only end up with a secret ref its instance-level
// ceiling also granted. A nil/empty ceiling yields an empty result (grant
// nothing), never a pass-through.
func intersectRefs(requested, ceiling map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(requested))
	for ref := range requested {
		if _, ok := ceiling[ref]; ok {
			out[ref] = struct{}{}
		}
	}
	return out
}

// ResolvePolicy computes the effective per-processor policy: the intersection of
// the pipeline-author's per-processor allowlist with the operator's engine-level
// ceiling. Entries a pipeline requests that the ceiling does not permit are
// DROPPED (returned in dropped, for a startup warning) — never silently honored.
//
//   - If the ceiling is not enabled (the stock deny-all engine default), the
//     result is deny-all regardless of what the pipeline requests: egress needs
//     an affirmative operator action at the instance level too (defense in depth
//     for shared/multi-tenant instances).
//   - If ceiling.Allowlist is empty but ceiling.Enabled is true, the ceiling is
//     "honor whatever the pipeline requests" (single-tenant / laptop convenience).
//
// The guest cannot widen its own policy: this runs host-side from operator-authored
// config, and the result is bound per-instance and never re-read from guest input.
func ResolvePolicy(perProcessor Policy, ceiling Policy) (effective Policy, dropped []AllowEntry) {
	if !perProcessor.Enabled {
		return DenyAll(), nil
	}
	if !ceiling.Enabled {
		// Engine ceiling closed: the pipeline opt-in cannot override it.
		return DenyAll(), perProcessor.Allowlist
	}

	eff := Policy{
		Enabled:          true,
		SecretRefs:       perProcessor.SecretRefs,
		Timeout:          perProcessor.Timeout,
		MaxResponseBytes: perProcessor.MaxResponseBytes,
	}
	if eff.Timeout <= 0 {
		eff.Timeout = DefaultTimeout
	}
	if eff.MaxResponseBytes <= 0 {
		eff.MaxResponseBytes = DefaultMaxResponseBytes
	}

	// Ceiling caps are tightening-only: a positive ceiling Timeout/MaxResponseBytes
	// is an operator-set upper bound no pipeline can exceed. A per-processor value
	// larger than the ceiling is clamped down; a smaller one is kept. A zero
	// ceiling value means "no cap" (the per-processor value / defaults stand).
	// This runs before both the unrestricted and restricted branches below, so the
	// cap holds regardless of allowlist shape.
	if ceiling.Timeout > 0 && eff.Timeout > ceiling.Timeout {
		eff.Timeout = ceiling.Timeout
	}
	if ceiling.MaxResponseBytes > 0 && eff.MaxResponseBytes > ceiling.MaxResponseBytes {
		eff.MaxResponseBytes = ceiling.MaxResponseBytes
	}

	// Ceiling with no allowlist entries but enabled == unrestricted host set
	// (single-tenant convenience). Secrets still honor an explicit ceiling scope:
	// if the operator granted a specific secret-refs set, clamp to it even here —
	// otherwise a ceiling that restricts secrets but not hosts would leak
	// ungranted refs. With no ceiling secret-refs, this stays a pass-through
	// (nothing to enforce).
	if len(ceiling.Allowlist) == 0 {
		eff.Allowlist = perProcessor.Allowlist
		if len(ceiling.SecretRefs) > 0 {
			eff.SecretRefs = intersectRefs(perProcessor.SecretRefs, ceiling.SecretRefs)
		}
		return eff, nil
	}

	// Restricted (multi-tenant) ceiling: clamp SecretRefs to the ceiling's granted
	// set exactly as the allowlist is clamped, so one tenant cannot name a secret
	// the operator did not grant this instance. An ungranted ref is dropped here
	// (defense in depth) and would additionally fail closed per-request in
	// buildHTTPRequest. A restricted ceiling that grants no secrets clamps to none
	// (explicit-grants-only).
	eff.SecretRefs = intersectRefs(perProcessor.SecretRefs, ceiling.SecretRefs)

	ceilingSet := make(map[string]struct{}, len(ceiling.Allowlist))
	for _, e := range ceiling.Allowlist {
		ceilingSet[entryKey(e)] = struct{}{}
	}
	for _, e := range perProcessor.Allowlist {
		if _, ok := ceilingSet[entryKey(e)]; ok {
			eff.Allowlist = append(eff.Allowlist, e)
		} else {
			dropped = append(dropped, e)
		}
	}
	if len(eff.Allowlist) == 0 {
		// Nothing survived the intersection: effectively deny-all, but keep
		// Enabled=true so a Do returns a specific forbidden error per call rather
		// than the "egress disabled" error — the operator DID opt in, the entries
		// were just clamped away.
		eff.Allowlist = nil
	}
	return eff, dropped
}
