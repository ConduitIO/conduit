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

	"github.com/matryer/is"
)

// TestRefuse_RefusedRanges is the parameterized refused-range table (design
// § Testing). It proves the Stage-2 classifier refuses every private, loopback,
// link-local, reserved, and embedded-v4 range — including the metadata endpoint,
// the v4-mapped form, and the NAT64/6to4/Teredo/v4-compatible embedded forms.
func TestRefuse_RefusedRanges(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		// IPv4 floor
		{"v4 loopback", "127.0.0.1"},
		{"v4 loopback high", "127.255.255.254"},
		{"v4 any", "0.0.0.0"},
		{"v4 private 10", "10.1.2.3"},
		{"v4 private 172.16", "172.16.5.5"},
		{"v4 private 172.31", "172.31.255.255"},
		{"v4 private 192.168", "192.168.1.1"},
		{"v4 link-local", "169.254.1.1"},
		{"v4 METADATA 169.254.169.254", "169.254.169.254"},
		{"v4 cgnat", "100.64.0.1"},
		{"v4 multicast", "224.0.0.1"},
		{"v4 broadcast", "255.255.255.255"},
		// IPv6 floor
		{"v6 loopback", "::1"},
		{"v6 unspecified", "::"},
		{"v6 link-local", "fe80::1"},
		{"v6 ULA", "fc00::1"},
		{"v6 ULA fd", "fd12:3456::1"},
		{"v6 multicast", "ff02::1"},
		// embedded-v4 forms — the encoding-trick / DNS64 defenses
		{"v4-mapped metadata", "::ffff:169.254.169.254"},
		{"v4-mapped private", "::ffff:10.0.0.1"},
		{"v4-mapped loopback", "::ffff:127.0.0.1"},
		{"NAT64 metadata (64:ff9b::a9fe:a9fe)", "64:ff9b::a9fe:a9fe"},
		{"NAT64 private", "64:ff9b::0a00:0001"},
		{"NAT64 public-embedded still refused wholesale", "64:ff9b::5db8:d822"},
		{"6to4 embedding metadata", "2002:a9fe:a9fe::1"},
		{"6to4 embedding public still refused wholesale", "2002:5db8:d822::1"},
		{"Teredo", "2001::1"},
		{"Teredo full", "2001:0:4136:e378:8000:63bf:3fff:fdd2"},
		{"v4-compatible metadata", "::169.254.169.254"},
		// Regression: red-team findings #1/#2 — the two encoding-gap ranges the
		// classifier previously let through.
		{"v4-translated metadata (::ffff:0:169.254.169.254)", "::ffff:0:169.254.169.254"},
		{"v4-translated private", "::ffff:0:10.0.0.1"},
		{"v4-translated prefix base", "::ffff:0:0:0"},
		{"v6 site-local (deprecated fec0::/10)", "fec0::1"},
		{"v6 site-local high", "fecf:ffff::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ip := net.ParseIP(tc.ip)
			is.True(ip != nil) // test IP must parse
			refused, reason := Refuse(ip)
			is.True(refused)                    // must be refused
			is.True(reason != reasonNotRefused) // must carry a reason
		})
	}
}

// TestRefuse_AllowedRanges proves the classifier does NOT over-block ordinary
// public unicast addresses (both families), so egress to real providers works.
func TestRefuse_AllowedRanges(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"public v4 (OpenAI-ish)", "104.18.0.1"},
		{"public v4 TEST-NET-3 (non-refused)", "203.0.113.10"},
		{"public v4 8.8.8.8", "8.8.8.8"},
		{"public v6", "2606:4700::1"},
		{"v4-mapped public", "::ffff:104.18.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ip := net.ParseIP(tc.ip)
			is.True(ip != nil)
			refused, _ := Refuse(ip)
			is.True(!refused) // must be allowed by range
		})
	}
}

// TestRefuse_NAT64Unwrap proves the NAT64-embedded IPv4 is extracted and that it
// is the metadata endpoint — the load-bearing DNS64 defense (MUST-FIX 2).
func TestRefuse_NAT64Unwrap(t *testing.T) {
	is := is.New(t)
	ip := net.ParseIP("64:ff9b::a9fe:a9fe")
	is.True(ip != nil)
	embedded := embeddedV4(ip)
	is.True(embedded != nil)
	is.Equal(embedded.To4().String(), "169.254.169.254") // unwraps to metadata IP
	refused, reason := Refuse(ip)
	is.True(refused)
	is.Equal(reason, reasonNAT64)
}

func TestRefuse_NilAndEmpty(t *testing.T) {
	is := is.New(t)
	refused, _ := Refuse(nil)
	is.True(refused)
	refused, _ = Refuse(net.IP{})
	is.True(refused)
}

// FuzzRefuse fuzzes the refused-range classifier over arbitrary 4- and 16-byte
// address inputs. The invariant asserted: Refuse never panics, and any address
// whose (possibly-unwrapped) IPv4 form is in a refused v4 range is refused —
// i.e. an embedded-v4 encoding can never smuggle a private/metadata address past
// the gate. (Design § Testing: "fuzz the refused-range classifier incl the
// v4-mapped and NAT64 unwrap paths".)
func FuzzRefuse(f *testing.F) {
	seeds := [][]byte{
		net.ParseIP("169.254.169.254").To4(),
		net.ParseIP("::ffff:169.254.169.254").To16(),
		net.ParseIP("64:ff9b::a9fe:a9fe").To16(),
		net.ParseIP("2002:a9fe:a9fe::1").To16(),
		net.ParseIP("::ffff:0:169.254.169.254").To16(), // v4-translated (finding #1)
		net.ParseIP("fec0::1").To16(),                  // deprecated site-local (finding #2)
		net.ParseIP("8.8.8.8").To4(),
		net.ParseIP("2606:4700::1").To16(),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var ip net.IP
		switch len(raw) {
		case net.IPv4len, net.IPv6len:
			ip = net.IP(raw)
		default:
			return // only 4/16-byte inputs are valid net.IP
		}
		refused, _ := Refuse(ip) // must never panic

		// Cross-check: if the address (or its embedded v4) lands in a refused v4
		// range, the classifier MUST refuse it. This is the anti-SSRF invariant.
		var v4 net.IP
		if e := embeddedV4(ip); e != nil {
			v4 = e.To4()
		} else if t4 := ip.To4(); t4 != nil {
			v4 = t4
		}
		if v4 != nil {
			for _, r := range refusedV4 {
				if r.net.Contains(v4) && !refused {
					t.Fatalf("Refuse allowed %v whose v4 form %v is in refused range %v", ip, v4, r.net)
				}
			}
		}
	})
}
