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

package harness

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// ScrubCheck is the name a case declares in sanitized.check.
const ScrubCheck = "scrub-v1"

// SecretCanaryPrefix starts every placeholder that stands in for a secret
// value. A scrubbed input may carry a plaintext secret only as such a
// placeholder, which makes every placeholder a canary: the runner fails a
// case if one appears in any byte the migrator outputs.
const SecretCanaryPrefix = "PLACEHOLDER_SECRET_"

var (
	// secretKeyRe follows the base design's built-in secret list ("Secrets",
	// rule 1) and errs toward matching: a key wrongly treated as secret only
	// forces a placeholder into a corpus file.
	secretKeyRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|credentials?$|sasl\.jaas\.config|basic\.auth\.user\.info|(^|\.)key$|private\.key|keystore\.key|ssl\.key\.password)`)
	// embeddedPasswordRe finds password=<value> inside a value: JDBC URL
	// query strings and JAAS configs.
	embeddedPasswordRe = regexp.MustCompile(`(?i)password\s*=\s*\\?"?([^&;"\\\s]*)`)
	hostPortRe         = regexp.MustCompile(`^\[?([A-Za-z0-9_.:-]+?)\]?:(\d{1,5})$`)
	hostKeyRe          = regexp.MustCompile(`(?i)(hostname|\.host|(^|\.)servers|\.url|\.uris)$`)
	awsKeyIDRe         = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	tokenSplitRe       = regexp.MustCompile(`[,\s;'"]+`)
)

// documentation address ranges (RFC 5737, RFC 3849).
var docPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// ScrubFinding is one scrub-v1 violation. It never carries a secret value.
type ScrubFinding struct {
	File    string
	Key     string
	Line    int
	Pointer string
	Problem string
}

func (f ScrubFinding) String() string {
	loc := f.Pointer
	if f.Line > 0 {
		loc = fmt.Sprintf("line %d", f.Line)
	}
	return fmt.Sprintf("%s (%s, key %q): %s", f.File, loc, f.Key, f.Problem)
}

// Scrub runs scrub-v1 over parsed input files. It checks values only:
// comments in .properties files are not input and are not parsed.
func Scrub(files []InputFile) []ScrubFinding {
	var out []ScrubFinding
	for _, f := range files {
		for _, k := range f.Keys {
			for _, p := range scrubValue(k.Key, k.Value) {
				out = append(out, ScrubFinding{File: f.Path, Key: k.Key, Line: k.Line, Pointer: k.Pointer, Problem: p})
			}
		}
	}
	return out
}

func scrubValue(key, value string) []string {
	var problems []string
	if secretKeyRe.MatchString(key) && !isPlaceholderSecret(value) {
		problems = append(problems, "secret-named key has a value that is neither a config-provider reference nor a "+SecretCanaryPrefix+"* placeholder")
	}
	for _, m := range embeddedPasswordRe.FindAllStringSubmatch(value, -1) {
		if !isPlaceholderSecret(m[1]) {
			problems = append(problems, "embedded password= value is not a placeholder")
		}
	}
	if awsKeyIDRe.MatchString(value) {
		problems = append(problems, "value contains an AWS access key id")
	}
	if strings.Contains(value, "-----BEGIN") {
		problems = append(problems, "value contains a PEM block")
	}
	for _, h := range hostsIn(key, value) {
		if !allowedHost(h) {
			problems = append(problems, fmt.Sprintf("host %q is outside example.*, *.example and the documentation IP ranges", h))
		}
	}
	return problems
}

func isPlaceholderSecret(v string) bool {
	return v == "" || strings.HasPrefix(v, "${") || strings.Contains(v, SecretCanaryPrefix)
}

// hostsIn extracts host names from URLs, host:port tokens, and the plain
// values of host-like keys.
func hostsIn(key, value string) []string {
	var hosts []string
	hostKey := hostKeyRe.MatchString(key)
	for _, tok := range tokenSplitRe.Split(value, -1) {
		if tok == "" || strings.HasPrefix(tok, "${") {
			continue
		}
		if i := strings.Index(tok, "://"); i >= 0 {
			if h := urlHost(tok[i+3:]); h != "" {
				hosts = append(hosts, h)
			}
			continue
		}
		if m := hostPortRe.FindStringSubmatch(tok); m != nil {
			hosts = append(hosts, m[1])
			continue
		}
		if hostKey && !strings.ContainsAny(tok, "/=") {
			hosts = append(hosts, tok)
		}
	}
	return hosts
}

func urlHost(rest string) string {
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if strings.HasPrefix(rest, "[") {
		if i := strings.Index(rest, "]"); i > 0 {
			return rest[1:i]
		}
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[:i], ":") {
		rest = rest[:i]
	}
	return rest
}

func allowedHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if addr, err := netip.ParseAddr(h); err == nil {
		for _, p := range docPrefixes {
			if p.Contains(addr) {
				return true
			}
		}
		return false
	}
	return h == "example" || strings.HasSuffix(h, ".example") ||
		strings.HasPrefix(h, "example.") || strings.HasSuffix(h, ".example.com") ||
		strings.HasSuffix(h, ".example.org") || strings.HasSuffix(h, ".example.net")
}
