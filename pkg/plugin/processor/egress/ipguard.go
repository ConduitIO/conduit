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

import "net"

// refusedReason is a stable, log-safe label for why a resolved IP was refused.
// It is emitted in the audit log (never the request/response body).
type refusedReason string

const (
	reasonNotRefused   refusedReason = ""
	reasonUnparseable  refusedReason = "unparseable_ip"
	reasonV4Loopback   refusedReason = "v4_loopback_or_any"
	reasonV4Private    refusedReason = "v4_private"
	reasonV4LinkLocal  refusedReason = "v4_link_local_or_metadata"
	reasonV4CGNAT      refusedReason = "v4_cgnat"
	reasonV6Loopback   refusedReason = "v6_loopback_or_unspecified"
	reasonV6LinkLocal  refusedReason = "v6_link_local"
	reasonV6SiteLocal  refusedReason = "v6_site_local"
	reasonV6ULA        refusedReason = "v6_ula"
	reasonV4Mapped     refusedReason = "v4_mapped_embedded"
	reasonV4Compatible refusedReason = "v4_compatible_embedded"
	reasonV4Translated refusedReason = "v4_translated_embedded"
	reasonNAT64        refusedReason = "nat64_embedded_v4"
	reasonSixToFour    refusedReason = "6to4_embedded_v4"
	reasonTeredo       refusedReason = "teredo_embedded_v4"
	reasonMulticastEtc refusedReason = "reserved_or_multicast"
)

// mustCIDR parses a CIDR at init and panics on error; the inputs are compile-time
// constants, so a parse failure is a programming error, not a runtime condition.
func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("egress: invalid built-in CIDR " + s + ": " + err.Error())
	}
	return n
}

// refusedV4 is the fixed IPv4 floor: loopback/any, RFC 1918 private, link-local
// (incl. the 169.254.169.254 metadata endpoint), and CGNAT. This floor is
// non-negotiable per the design; the classifier may only ever get stricter.
var refusedV4 = []struct {
	net    *net.IPNet
	reason refusedReason
}{
	{mustCIDR("127.0.0.0/8"), reasonV4Loopback},
	{mustCIDR("0.0.0.0/8"), reasonV4Loopback},
	{mustCIDR("10.0.0.0/8"), reasonV4Private},
	{mustCIDR("172.16.0.0/12"), reasonV4Private},
	{mustCIDR("192.168.0.0/16"), reasonV4Private},
	{mustCIDR("169.254.0.0/16"), reasonV4LinkLocal}, // includes 169.254.169.254 (metadata)
	{mustCIDR("100.64.0.0/10"), reasonV4CGNAT},
}

// refusedV6 is the fixed IPv6 floor for genuinely-v6 addresses (loopback,
// unspecified, link-local, ULA). Embedded-v4 forms are handled separately in
// Refuse by unwrapping to their v4 representation and re-running the v4 floor.
var refusedV6 = []struct {
	net    *net.IPNet
	reason refusedReason
}{
	{mustCIDR("::1/128"), reasonV6Loopback},
	{mustCIDR("::/128"), reasonV6Loopback},
	{mustCIDR("fe80::/10"), reasonV6LinkLocal},
	{mustCIDR("fec0::/10"), reasonV6SiteLocal}, // RFC 3879 deprecated site-local, still parseable/private
	{mustCIDR("fc00::/7"), reasonV6ULA},
}

var (
	nat64Net = mustCIDR("64:ff9b::/96") // RFC 6052 well-known NAT64 prefix
	// v4TranslatedNet is the RFC 6052 IPv4-Translated special-purpose prefix. It
	// is DISTINCT from the v4-mapped ::ffff:0:0/96 that ip.To4() unwraps: the
	// embedded v4 sits one 16-bit group further right (bytes [12:16] with ffff at
	// bytes [8:9]), so To4() returns nil for it and classifyV4 never sees it. A
	// DNS64/SIIT translator can synthesize ::ffff:0:<metadata-v4> to smuggle a
	// private/metadata dial past a gate that only unwraps v4-mapped — refuse the
	// whole block wholesale, in the same threat class as NAT64/6to4/Teredo.
	v4TranslatedNet = mustCIDR("::ffff:0:0:0/96")
)

// classifyV4 runs the IPv4 refused-range floor on a 4-byte (or v4-mapped) IP.
func classifyV4(ip net.IP) refusedReason {
	v4 := ip.To4()
	if v4 == nil {
		return reasonUnparseable
	}
	for _, r := range refusedV4 {
		if r.net.Contains(v4) {
			return r.reason
		}
	}
	// Also refuse anything not a global unicast address that slipped through
	// (multicast 224.0.0.0/4, broadcast 255.255.255.255).
	if v4[0] >= 224 {
		return reasonMulticastEtc
	}
	return reasonNotRefused
}

// Refuse reports whether an IP about to be dialed must be refused, and why.
//
// It is the single classifier used by the dial-time Control hook. It runs on
// the parsed net.IP (not the textual form), so decimal/octal/hex encodings are
// irrelevant — they all parse to the same address bytes. Embedded-v4 IPv6 forms
// (v4-mapped ::ffff:, v4-compatible ::/96, NAT64 64:ff9b::/96, 6to4 2002::/16,
// Teredo 2001::/32) are unwrapped to their v4 form and re-checked against the v4
// floor, and the synthesized-prefix blocks (NAT64/6to4/Teredo/v4-compatible) are
// refused wholesale as a belt-and-braces default — no legitimate egress target
// is reached only via a synthesized address. reasonNotRefused ("") means allowed
// by range (the caller still applies the explicit-(IP,port) carve-out).
//
// Invariant (design § Security boundary, Stage 2): every candidate resolved IP
// is classified here at dial time; the metadata endpoint (169.254.169.254) and
// all private/loopback ranges are refused unless an explicit (IP, port)
// carve-out admits the exact pair.
func Refuse(ip net.IP) (bool, refusedReason) {
	if ip == nil {
		return true, reasonUnparseable
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return true, reasonUnparseable
	}

	// IPv4 or IPv4-mapped IPv6 (::ffff:a.b.c.d): To4 returns the 4-byte form for
	// both, so this branch covers the v4-mapped unwrap for free.
	if v4 := ip.To4(); v4 != nil {
		if r := classifyV4(v4); r != reasonNotRefused {
			// Distinguish a mapped form for the audit log.
			if len(ip) == net.IPv6len && !isRawV4(ip) {
				return true, reasonV4Mapped
			}
			return true, r
		}
		return false, reasonNotRefused
	}

	// From here the address is genuinely IPv6.

	// NAT64 64:ff9b::/96 — a DNS64 resolver (or a hostile authoritative server)
	// can legitimately synthesize 64:ff9b::a9fe:a9fe which EMBEDS 169.254.169.254.
	// Unwrap the embedded v4 and re-check, and refuse the whole block outright.
	if nat64Net.Contains(ip16) {
		return true, reasonNAT64
	}
	// IPv4-Translated ::ffff:0:0:0/96 (RFC 6052) — embeds a v4 in bytes [12:16]
	// that To4() does NOT unwrap (ffff sits at [8:9], not [10:11]); refuse
	// wholesale so a synthesized ::ffff:0:<private-v4> cannot slip the gate.
	if v4TranslatedNet.Contains(ip16) {
		return true, reasonV4Translated
	}
	// 6to4 2002::/16 embeds the v4 in bytes [2:6]; refuse wholesale.
	if ip16[0] == 0x20 && ip16[1] == 0x02 {
		return true, reasonSixToFour
	}
	// Teredo 2001:0000::/32 embeds a v4 (obfuscated) client/server address; refuse wholesale.
	if ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0x00 && ip16[3] == 0x00 {
		return true, reasonTeredo
	}
	// IPv4-compatible ::/96 (first 12 bytes zero, not :: or ::1): deprecated,
	// unwrap the embedded v4 and refuse wholesale.
	if isV4Compatible(ip16) {
		return true, reasonV4Compatible
	}

	for _, r := range refusedV6 {
		if r.net.Contains(ip16) {
			return true, r.reason
		}
	}
	// Refuse multicast (ff00::/8) and other non-global scopes defensively.
	if ip16[0] == 0xff {
		return true, reasonMulticastEtc
	}
	return false, reasonNotRefused
}

// isRawV4 reports whether ip is stored as a native 4-byte IPv4 (vs a 16-byte
// v4-mapped form), used only to label the audit reason.
func isRawV4(ip net.IP) bool { return len(ip) == net.IPv4len }

// isV4Compatible reports whether ip16 is an IPv4-compatible IPv6 address
// (::a.b.c.d, first 12 bytes zero) other than :: and ::1.
func isV4Compatible(ip16 net.IP) bool {
	for i := 0; i < 12; i++ {
		if ip16[i] != 0 {
			return false
		}
	}
	// exclude :: (all zero) and ::1 — those are caught by the v6 loopback floor,
	// but if this runs first, still unwrap: last 4 bytes 0.0.0.0 / 0.0.0.1 are in
	// 0.0.0.0/8 so they'd be refused anyway. Treat anything with a non-trivial
	// embedded v4 as v4-compatible.
	last4 := ip16[12:16]
	return last4[0] != 0 || last4[1] != 0 || last4[2] != 0 || (last4[3] != 0 && last4[3] != 1)
}

// embeddedV4 extracts the IPv4 address embedded in a synthesized IPv6 form, for
// audit logging. Returns nil if none is embedded.
func embeddedV4(ip net.IP) net.IP {
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	switch {
	case nat64Net.Contains(ip16):
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	case v4TranslatedNet.Contains(ip16): // RFC 6052 IPv4-translated: embedded v4 in [12:16]
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	case ip16[0] == 0x20 && ip16[1] == 0x02: // 6to4
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5])
	case isV4Compatible(ip16):
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	default:
		return nil
	}
}
