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
	"slices"
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
	secretKeyRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|credentials?$|sasl\.jaas\.config|basic\.auth\.user\.info|(^|\.)key$|private\.key|keystore\.key)`)
	// canaryRe and referenceRe are the only two accepted forms of a secret
	// value, matched exactly.
	canaryRe    = regexp.MustCompile(`^` + SecretCanaryPrefix + `[A-Z0-9_]+$`)
	referenceRe = regexp.MustCompile(`^\$\{[^{}\s]+\}$`)
	// embeddedPasswordRe finds password=<value> inside a value or a raw file:
	// JDBC URL query strings and JAAS configs, quoted or JSON-escaped.
	embeddedPasswordRe = regexp.MustCompile(`(?i)password\s*=\s*(?:\\?")?([^&;"\\\s]*)`)
	// urlRe finds scheme://authority anywhere, including jdbc:mysql://.
	urlRe      = regexp.MustCompile(`(?i)[a-z][a-z0-9+.:-]*://([^/?#\s,;"'<>\\]*)`)
	hostPortRe = regexp.MustCompile(`^\[?([A-Za-z0-9_.:-]+?)\]?:(\d{1,5})$`)
	hostKeyRe  = regexp.MustCompile(`(?i)(hostname|\.host|(^|\.)servers|\.url|\.uris)$`)
	// fqdnRe is a bare dotted name; isBareHost decides whether its last
	// label is a TLD. Java class names (org.apache.kafka...) end in a class.
	fqdnRe = regexp.MustCompile(`(?i)^[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	// jsonMemberRe finds a quoted JSON member key and its scalar value at any
	// depth of a raw file: a string (escapes kept) or a number/bool literal.
	jsonMemberRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*:\s*(?:"((?:[^"\\]|\\.)*)"|(-?[0-9][0-9.eE+-]*|true|false))`)
	jsonKeyRe    = regexp.MustCompile(`"(?:[^"\\]|\\.)*"\s*:`)
	propKeyRe    = regexp.MustCompile(`^\s*[A-Za-z0-9_.>-]+\s*[=:]`)
	// escapedMemberRe is jsonMemberRe one level of string escaping down:
	// \"key\": \"value\" inside a JSON string value.
	escapedMemberRe = regexp.MustCompile(`\\"([^"\\]*)\\"\s*:\s*(?:\\"([^"\\]*)\\"|(-?[0-9][0-9.eE+-]*|true|false))`)
	// keyValueRe finds key=value and key: value in free text (comments).
	keyValueRe = regexp.MustCompile(`([A-Za-z0-9_.-]+)\s*[=:]\s*([^\s,;"']+)`)
	// queryTokenRe finds credential-like URL query parameters.
	queryTokenRe = regexp.MustCompile(`(?i)[?&](token|access_token|auth_token|api_key|apikey|key|secret|sig|signature|password|pwd)=([^&\s"'#]*)`)
	// kafkaInternalTopicRe are MirrorMaker internal topic names, which look
	// like hosts under .internal and appear in upstream comments. Exempt only
	// as bare tokens in comment text (scrubText), never in allowedHost.
	kafkaInternalTopicRe = regexp.MustCompile(`(?i)^(mm2-(offsets|configs|status|offset-syncs)\.[a-z0-9_-]+|[a-z0-9_-]+\.checkpoints|[a-z0-9_-]+\.heartbeats|heartbeats)\.internal$`)
	ipv4Re               = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
	awsKeyIDRe           = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	tokenSplitRe         = regexp.MustCompile(`[,\s;'"]+`)
	rawSplitRe           = regexp.MustCompile(`[\s,;'"=()\[\]{}<>\\|]+`)
)

// fqdnTLDs are the generic TLDs a bare name is treated as a host for. Every
// two-letter alphabetic last label also counts (country codes). A closed
// list on purpose: inverting it would make every dotted value a host
// (inventory.orders, schema-changes.inventory are table and topic names).
// File names (mysql.properties, report.json) and Java packages end in labels
// that are on neither list.
var fqdnTLDs = []string{
	"com", "net", "org", "edu", "gov", "mil", "int", "info", "biz", "xyz", "io", "dev", "cloud", "app", "ai",
	"online", "site", "tech", "store", "shop", "host", "cc", "tv", "me", "pro", "systems", "network",
	"services", "solutions", "digital", "group", "global", "company", "zone", "space", "link", "page", "run",
	"internal", "local", "lan", "corp", "intra", "home", "svc", "cluster", "amazonaws", "example",
}

// publicDocHosts may appear in upstream license headers and comments of a
// raw file. They are public documentation sites, never customer
// infrastructure, and are accepted only by the raw-file scan.
var publicDocHosts = []string{
	"www.apache.org", "apache.org", "kafka.apache.org", "cwiki.apache.org", "issues.apache.org",
	"github.com", "debezium.io", "docs.confluent.io", "www.confluent.io",
}

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
	if f.Key == "" {
		return fmt.Sprintf("%s (raw line %d): %s", f.File, f.Line, f.Problem)
	}
	loc := f.Pointer
	if f.Line > 0 {
		loc = fmt.Sprintf("line %d", f.Line)
	}
	return fmt.Sprintf("%s (%s, key %q): %s", f.File, loc, f.Key, f.Problem)
}

// Scrub runs the parsed-value half of scrub-v1 over parsed input files.
// ScrubRaw is the other half; a corpus file must pass both.
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

// ScrubRaw scans the whole file as text, line by line, independent of any
// parser: comments, nested and non-string JSON values, REST-export status
// and task traces all get the same host, URL, password, key-id and PEM
// checks, and every secret-named key, as a quoted JSON member at any depth
// or as key=value / key: value in free text, must hold a placeholder. It is
// what catches what the parsed-value check never sees.
func ScrubRaw(name string, b []byte) []ScrubFinding {
	var out []ScrubFinding
	for i, line := range splitNaturalLines(string(b)) {
		for _, p := range scrubText(line) {
			out = append(out, ScrubFinding{File: name, Line: i + 1, Problem: p})
		}
	}
	return append(out, jsonSecretMembers(name, string(b))...)
}

func scrubValue(key, value string) []string {
	var problems []string
	embedded := embeddedPasswordRe.MatchString(value)
	// A secret-named key must hold exactly a canary or a reference. A value
	// that carries password=... (a JAAS config) is judged by its embedded
	// passwords instead, below.
	if secretKeyRe.MatchString(key) && !embedded && !isPlaceholderSecret(value) {
		problems = append(problems, "secret-named key has a value that is neither a config-provider reference nor a "+SecretCanaryPrefix+"* placeholder")
	}
	problems = append(problems, secretsInText(value)...)
	for _, h := range hostsInValue(key, value) {
		if !allowedHost(h) {
			problems = append(problems, fmt.Sprintf("host %q is outside *.example, example.com/.org/.net and the documentation IP ranges", h))
		}
	}
	return problems
}

// jsonSecretMembers applies the secret-key rule to every quoted JSON member
// of the whole text, so a member split across lines is still seen, and to
// every escaped member (\"key\": \"value\"), so a config embedded as an
// escaped JSON string is seen too.
func jsonSecretMembers(name, text string) []ScrubFinding {
	var out []ScrubFinding
	for _, re := range []*regexp.Regexp{jsonMemberRe, escapedMemberRe} {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			key := text[m[2]:m[3]]
			if !secretKeyRe.MatchString(key) {
				continue
			}
			var problem string
			switch {
			case m[6] >= 0: // a number or boolean under a secret key is a plaintext secret
				problem = fmt.Sprintf("secret-named JSON member %q has a literal value", key)
			case !isPlaceholderSecret(text[m[4]:m[5]]) && !embeddedPasswordRe.MatchString(text[m[4]:m[5]]):
				problem = fmt.Sprintf("secret-named JSON member %q is not a placeholder", key)
			default:
				continue
			}
			out = append(out, ScrubFinding{File: name, Line: strings.Count(text[:m[0]], "\n") + 1, Problem: problem})
		}
	}
	return out
}

// isCommentLine is a .properties comment (or shell-style) line.
func isCommentLine(line string) bool {
	t := strings.TrimLeft(line, " \t\f")
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!")
}

func scrubText(line string) []string {
	problems := secretsInText(line)
	problems = append(problems, secretKeysInText(line)...)
	comment := isCommentLine(line)
	var hosts []string
	for _, m := range urlRe.FindAllStringSubmatch(line, -1) {
		if h := authorityHost(m[1]); h != "" {
			hosts = append(hosts, h)
		}
	}
	// Config keys are names, not hosts (database.server.id, flush.interval.ms):
	// drop quoted JSON member keys and a leading properties key first.
	text := jsonKeyRe.ReplaceAllString(urlRe.ReplaceAllString(line, " "), " ")
	text = propKeyRe.ReplaceAllString(text, " ")
	for _, tok := range rawSplitRe.Split(text, -1) {
		tok = strings.Trim(tok, ".:/")
		switch {
		case tok == "" || strings.HasPrefix(tok, "$"):
		case hostPortRe.MatchString(tok):
			hosts = append(hosts, hostPortRe.FindStringSubmatch(tok)[1])
		case comment && kafkaInternalTopicRe.MatchString(tok):
			// A MirrorMaker internal topic named in an upstream comment. Only
			// a bare token in comment text: with a port, in a URL or in a
			// value it is a host and is checked.
		case isBareHost(tok):
			hosts = append(hosts, tok)
		}
	}
	for _, h := range hosts {
		if !allowedHost(h) && !slices.Contains(publicDocHosts, strings.ToLower(h)) && h != "0.0.0.0" {
			problems = append(problems, fmt.Sprintf("host %q is outside *.example, example.com/.org/.net and the documentation IP ranges", h))
		}
	}
	return problems
}

// secretKeysInText applies the secret-key rule to key=value / key: value
// pairs in a non-JSON line (comments, properties). JSON members are checked
// over the whole file by jsonSecretMembers.
func secretKeysInText(line string) []string {
	if jsonKeyRe.MatchString(line) {
		return nil
	}
	var problems []string
	for _, m := range keyValueRe.FindAllStringSubmatch(line, -1) {
		key, v := m[1], strings.TrimRight(m[2], ".")
		// A JAAS config's value is a login-module string; its password= is
		// checked by secretsInText.
		if !secretKeyRe.MatchString(key) || strings.Contains(strings.ToLower(key), "jaas") {
			continue
		}
		if !isPlaceholderSecret(v) {
			problems = append(problems, fmt.Sprintf("secret-named key %q has a value that is not a placeholder", key))
		}
	}
	return problems
}

// secretsInText finds secrets that need no key name: embedded password=
// values, passwords in URL userinfo, AWS key ids and PEM blocks.
func secretsInText(s string) []string {
	var problems []string
	for _, m := range embeddedPasswordRe.FindAllStringSubmatch(s, -1) {
		if !isPlaceholderSecret(m[1]) {
			problems = append(problems, "embedded password= value is not a placeholder")
		}
	}
	for _, m := range urlRe.FindAllStringSubmatch(s, -1) {
		if i := strings.LastIndex(m[1], "@"); i >= 0 {
			userinfo := m[1][:i]
			if j := strings.Index(userinfo, ":"); j >= 0 && !isPlaceholderSecret(userinfo[j+1:]) {
				problems = append(problems, "URL userinfo carries a password that is not a placeholder")
			}
		}
	}
	for _, m := range queryTokenRe.FindAllStringSubmatch(s, -1) {
		if !isPlaceholderSecret(m[2]) {
			problems = append(problems, fmt.Sprintf("URL query parameter %s carries a value that is not a placeholder", m[1]))
		}
	}
	if awsKeyIDRe.MatchString(s) {
		problems = append(problems, "contains an AWS access key id")
	}
	if strings.Contains(s, "-----BEGIN") {
		problems = append(problems, "contains a PEM block")
	}
	return problems
}

// isPlaceholderSecret accepts exactly a canary, exactly one config-provider
// reference, or an empty value.
func isPlaceholderSecret(v string) bool {
	return v == "" || canaryRe.MatchString(v) || referenceRe.MatchString(v)
}

// hostsInValue extracts host names from URLs, host:port tokens, bare
// dotted names with a host-like TLD, and the plain values of host-like keys.
func hostsInValue(key, value string) []string {
	var hosts []string
	hostKey := hostKeyRe.MatchString(key)
	for _, m := range urlRe.FindAllStringSubmatch(value, -1) {
		if h := authorityHost(m[1]); h != "" {
			hosts = append(hosts, h)
		}
	}
	for _, tok := range tokenSplitRe.Split(urlRe.ReplaceAllString(value, " "), -1) {
		switch {
		case tok == "" || strings.HasPrefix(tok, "${"):
		case hostPortRe.MatchString(tok):
			hosts = append(hosts, hostPortRe.FindStringSubmatch(tok)[1])
		case hostKey && !strings.ContainsAny(tok, "/="):
			hosts = append(hosts, tok)
		case isBareHost(tok):
			hosts = append(hosts, tok)
		}
	}
	return hosts
}

func isBareHost(tok string) bool {
	if ipv4Re.MatchString(tok) {
		return true
	}
	if !fqdnRe.MatchString(tok) {
		return false
	}
	tld := strings.ToLower(tok[strings.LastIndex(tok, ".")+1:])
	if len(tld) == 2 && strings.Trim(tld, "abcdefghijklmnopqrstuvwxyz") == "" {
		return true
	}
	return slices.Contains(fqdnTLDs, tld)
}

// authorityHost returns the host of a URL authority (userinfo@host:port).
func authorityHost(auth string) string {
	if i := strings.LastIndex(auth, "@"); i >= 0 {
		auth = auth[i+1:]
	}
	if strings.HasPrefix(auth, "[") {
		if i := strings.Index(auth, "]"); i > 0 {
			return auth[1:i]
		}
	}
	if i := strings.LastIndex(auth, ":"); i >= 0 && !strings.Contains(auth[:i], ":") {
		auth = auth[:i]
	}
	return auth
}

// allowedHost accepts the reserved .example TLD (RFC 2606), example.com,
// example.org, example.net and their subdomains, and the documentation IP
// ranges. Nothing else: example.prod.acme.com is a real domain.
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
	if h == "example" || strings.HasSuffix(h, ".example") {
		return true
	}
	for _, d := range []string{"example.com", "example.org", "example.net"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}
