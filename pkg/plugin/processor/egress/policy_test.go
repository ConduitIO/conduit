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
	"testing"
	"time"

	"github.com/matryer/is"
)

func TestParseAllowEntry(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantErr    bool
		wantScheme string
		wantHost   string
		wantPort   string
		wantIP     bool
	}{
		{"host only", "api.openai.com", false, "https", "api.openai.com", "443", false},
		{"host:port", "api.openai.com:8443", false, "https", "api.openai.com", "8443", false},
		{"https scheme", "https://api.voyageai.com", false, "https", "api.voyageai.com", "443", false},
		{"http private ip ok (ollama)", "http://127.0.0.1:11434", false, "http", "127.0.0.1", "11434", true},
		{"https loopback ip", "https://127.0.0.1:11434", false, "https", "127.0.0.1", "11434", true},
		{"http public host rejected", "http://api.openai.com", true, "", "", "", false},
		{"http public ip rejected", "http://8.8.8.8:80", true, "", "", "", false},
		{"wildcard rejected", "*.openai.com", true, "", "", "", false},
		{"bad scheme rejected", "ftp://host", true, "", "", "", false},
		{"empty rejected", "", true, "", "", "", false},
		{"path rejected", "https://host/v1/embeddings", true, "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			e, err := ParseAllowEntry(tc.in)
			if tc.wantErr {
				is.True(err != nil)
				return
			}
			is.NoErr(err)
			is.Equal(e.Scheme, tc.wantScheme)
			is.Equal(e.Host, tc.wantHost)
			is.Equal(e.Port, tc.wantPort)
			is.Equal(e.IsIP(), tc.wantIP)
		})
	}
}

func TestPolicy_MatchHostPort_Stage1(t *testing.T) {
	is := is.New(t)
	allow, err := ParseAllowlist("api.openai.com, https://api.voyageai.com:8443, http://127.0.0.1:11434")
	is.NoErr(err)
	p := Policy{Enabled: true, Allowlist: allow}

	// allowed
	is.True(p.MatchHostPort("https", "api.openai.com", "443"))
	is.True(p.MatchHostPort("https", "api.voyageai.com", "8443"))
	is.True(p.MatchHostPort("http", "127.0.0.1", "11434"))
	// wrong port
	is.True(!p.MatchHostPort("https", "api.openai.com", "8443"))
	// wrong scheme
	is.True(!p.MatchHostPort("http", "api.openai.com", "443"))
	// host not listed
	is.True(!p.MatchHostPort("https", "evil.example.com", "443"))
	// voyage on default 443 not listed (only 8443)
	is.True(!p.MatchHostPort("https", "api.voyageai.com", "443"))
}

// TestPolicy_CarveOut_IPPortScoped is MUST-FIX 3: the carve-out keys on the
// (IP, port) PAIR, never the IP alone. 127.0.0.1:11434 must not admit
// 127.0.0.1:6379.
func TestPolicy_CarveOut_IPPortScoped(t *testing.T) {
	is := is.New(t)
	allow, err := ParseAllowlist("http://127.0.0.1:11434")
	is.NoErr(err)
	p := Policy{Enabled: true, Allowlist: allow}

	loopback := net.ParseIP("127.0.0.1")
	is.True(p.matchesCarveOut(loopback, "11434"))                  // the listed pair
	is.True(!p.matchesCarveOut(loopback, "6379"))                  // Redis — same IP, different port
	is.True(!p.matchesCarveOut(loopback, "5432"))                  // Postgres
	is.True(!p.matchesCarveOut(net.ParseIP("127.0.0.2"), "11434")) // different IP
}

// TestResolvePolicy_Clamping is the config-clamping test (design § Testing): a
// per-processor allowlist naming a host outside the engine ceiling yields the
// intersection (excess dropped); a closed ceiling forces deny-all; a non-opted-in
// processor stays deny-all.
func TestResolvePolicy_Clamping(t *testing.T) {
	is := is.New(t)

	mk := func(spec string) Policy {
		allow, err := ParseAllowlist(spec)
		is.NoErr(err)
		return Policy{Enabled: true, Allowlist: allow}
	}

	t.Run("deny-all when not opted in", func(t *testing.T) {
		is := is.New(t)
		eff, dropped := ResolvePolicy(DenyAll(), mk("api.openai.com"))
		is.True(!eff.Enabled)
		is.Equal(len(dropped), 0)
	})

	t.Run("closed ceiling forces deny-all", func(t *testing.T) {
		is := is.New(t)
		eff, dropped := ResolvePolicy(mk("api.openai.com"), DenyAll())
		is.True(!eff.Enabled)
		is.Equal(len(dropped), 1) // the requested entry is reported as dropped
	})

	t.Run("intersection drops excess", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com, api.voyageai.com")
		ceiling := mk("api.openai.com") // ceiling permits only openai
		eff, dropped := ResolvePolicy(perProc, ceiling)
		is.True(eff.Enabled)
		is.Equal(len(eff.Allowlist), 1)
		is.Equal(eff.Allowlist[0].Host, "api.openai.com")
		is.Equal(len(dropped), 1)
		is.Equal(dropped[0].Host, "api.voyageai.com") // excess dropped, not honored
	})

	t.Run("enabled ceiling with no entries is unrestricted", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		ceiling := Policy{Enabled: true} // enabled, no entries => honor pipeline
		eff, dropped := ResolvePolicy(perProc, ceiling)
		is.True(eff.Enabled)
		is.Equal(len(eff.Allowlist), 1)
		is.Equal(len(dropped), 0)
	})

	// Regression: red-team finding #3 — SecretRefs must be clamped to the ceiling
	// on a restricted (multi-tenant) ceiling, mirroring the allowlist intersection,
	// so a tenant cannot name a secret the operator did not grant this instance.
	t.Run("secret refs clamped by restricted ceiling", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		perProc.SecretRefs = map[string]struct{}{"openai_key": {}, "stolen_key": {}}
		ceiling := mk("api.openai.com")
		ceiling.SecretRefs = map[string]struct{}{"openai_key": {}} // only openai granted
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.True(eff.Enabled)
		_, granted := eff.SecretRefs["openai_key"]
		is.True(granted) // granted ref survives
		_, leaked := eff.SecretRefs["stolen_key"]
		is.True(!leaked) // ungranted ref clamped away
		is.Equal(len(eff.SecretRefs), 1)
	})

	t.Run("restricted ceiling granting no secrets clamps to none", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		perProc.SecretRefs = map[string]struct{}{"openai_key": {}}
		ceiling := mk("api.openai.com") // no SecretRefs granted
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.Equal(len(eff.SecretRefs), 0) // nothing granted => nothing survives
	})

	t.Run("unrestricted ceiling passes secret refs through", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		perProc.SecretRefs = map[string]struct{}{"openai_key": {}}
		ceiling := Policy{Enabled: true} // unrestricted single-tenant mode
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.Equal(len(eff.SecretRefs), 1) // convenience mode: no clamp
	})

	// Ceiling Timeout/MaxResponseBytes are tightening-only caps: a positive
	// ceiling value is an upper bound no pipeline can exceed.
	t.Run("ceiling caps timeout and size down", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		perProc.Timeout = 60 * time.Second
		perProc.MaxResponseBytes = 8 << 20
		ceiling := mk("api.openai.com")
		ceiling.Timeout = 5 * time.Second
		ceiling.MaxResponseBytes = 1 << 20
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.Equal(eff.Timeout, 5*time.Second)         // clamped down to the ceiling
		is.Equal(eff.MaxResponseBytes, int64(1<<20)) // clamped down to the ceiling
	})

	t.Run("ceiling does not raise a smaller per-processor value", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com")
		perProc.Timeout = 2 * time.Second
		perProc.MaxResponseBytes = 512 << 10
		ceiling := mk("api.openai.com")
		ceiling.Timeout = 30 * time.Second
		ceiling.MaxResponseBytes = 4 << 20
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.Equal(eff.Timeout, 2*time.Second)           // smaller per-processor value kept
		is.Equal(eff.MaxResponseBytes, int64(512<<10)) // smaller per-processor value kept
	})

	t.Run("zero ceiling cap means no cap (defaults stand)", func(t *testing.T) {
		is := is.New(t)
		perProc := mk("api.openai.com") // no Timeout/MaxResponseBytes set
		ceiling := mk("api.openai.com") // no cap set
		eff, _ := ResolvePolicy(perProc, ceiling)
		is.Equal(eff.Timeout, DefaultTimeout)                   // per-processor default stands
		is.Equal(eff.MaxResponseBytes, DefaultMaxResponseBytes) // per-processor default stands
	})
}

// FuzzParseAllowEntry fuzzes the allowlist-entry parser + Stage-1 matcher — a
// parser/decision boundary where an edge-case bug becomes an SSRF bypass (design
// § Testing). Invariants: never panic; and any successfully-parsed entry that is
// an http-scheme IP literal must classify as a refused (private/loopback) range
// — the parser must never admit an http entry for a public target.
func FuzzParseAllowEntry(f *testing.F) {
	for _, s := range []string{
		"api.openai.com", "api.openai.com:443", "https://a.b.c:8443",
		"http://127.0.0.1:11434", "*.evil.com", "ftp://x", "", "http://8.8.8.8",
		"[::1]:80", "https://[fc00::1]:443", "http://169.254.169.254",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		e, err := ParseAllowEntry(in) // must never panic
		if err != nil {
			return
		}
		if e.Scheme != "http" && e.Scheme != "https" {
			t.Fatalf("parsed entry has invalid scheme %q from %q", e.Scheme, in)
		}
		if e.Scheme == "http" {
			if e.IP == nil {
				t.Fatalf("http entry %q admitted with non-IP host", in)
			}
			if refused, _ := Refuse(e.IP); !refused {
				t.Fatalf("http entry %q admitted for non-private IP %v", in, e.IP)
			}
		}
		// A successfully-parsed entry must self-match Stage 1.
		p := Policy{Enabled: true, Allowlist: []AllowEntry{e}}
		if !p.MatchHostPort(e.Scheme, e.Host, e.Port) {
			t.Fatalf("entry %q does not self-match Stage 1", in)
		}
	})
}

func TestPolicyFromSettings(t *testing.T) {
	is := is.New(t)

	t.Run("no key => deny-all", func(t *testing.T) {
		is := is.New(t)
		p, err := PolicyFromSettings(map[string]string{"other": "x"})
		is.NoErr(err)
		is.True(!p.Enabled)
	})

	t.Run("full config", func(t *testing.T) {
		is := is.New(t)
		p, err := PolicyFromSettings(map[string]string{
			ConfigKeyAllow:            "api.openai.com, http://127.0.0.1:11434",
			ConfigKeyTimeout:          "10s",
			ConfigKeyMaxResponseBytes: "1048576",
			ConfigKeySecretRefs:       "openai_api_key, voyage_key",
		})
		is.NoErr(err)
		is.True(p.Enabled)
		is.Equal(len(p.Allowlist), 2)
		is.Equal(p.Timeout.String(), "10s")
		is.Equal(p.MaxResponseBytes, int64(1048576))
		_, ok := p.SecretRefs["openai_api_key"]
		is.True(ok)
	})

	t.Run("invalid allowlist fails", func(t *testing.T) {
		is := is.New(t)
		_, err := PolicyFromSettings(map[string]string{ConfigKeyAllow: "*.evil.com"})
		is.True(err != nil)
	})
}
