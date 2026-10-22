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
	"net"
	"strconv"
	"strings"
)

// metadataHosts are host names that resolve to a cloud metadata service.
var metadataHosts = map[string]struct{}{
	"metadata":                     {},
	"metadata.google.internal":     {},
	"metadata.goog":                {},
	"instance-data":                {},
	"instance-data.ec2.internal":   {},
	"metadata.tencentyun.com":      {},
	"metadata.platformequinix.com": {},
}

// IsMetadataHost reports whether host (already lower-cased, no trailing dot) is
// a well-known cloud metadata name.
func IsMetadataHost(host string) bool {
	_, ok := metadataHosts[host]
	return ok
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("connegress: invalid CIDR " + s + ": " + err.Error())
	}
	return n
}

// metadataNets are the addresses of metadata services: the IPv4 link-local /16
// (AWS and GCP 169.254.169.254, the ECS task credentials endpoint
// 169.254.170.2, Azure IMDS 169.254.169.254), the Azure WireServer, Alibaba
// Cloud, Oracle Cloud, the AWS IPv6 endpoint, and IPv6 link-local.
var metadataNets = []*net.IPNet{
	mustCIDR("169.254.0.0/16"),
	mustCIDR("168.63.129.16/32"),
	mustCIDR("100.100.100.200/32"),
	mustCIDR("192.0.0.192/32"), // Oracle Cloud Infrastructure instance metadata
	mustCIDR("fd00:ec2::/32"),
	mustCIDR("fe80::/10"),
}

var (
	nat64WellKnown = mustCIDR("64:ff9b::/96")
	nat64LocalUse  = mustCIDR("64:ff9b:1::/48")
	sixToFour      = mustCIDR("2002::/16")
	teredo         = mustCIDR("2001::/32")
)

// IsMetadataIP reports whether ip is, or embeds, a metadata address. IPv4-mapped
// (::ffff:a.b.c.d), NAT64 (64:ff9b::/96 and the local-use 64:ff9b:1::/48), 6to4
// and Teredo addresses are unwrapped to the IPv4 address they carry and checked
// too: a DNS64 translator can map the metadata address into any of them. Private
// and loopback ranges are not metadata and are not refused here.
func IsMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, c := range candidates(ip) {
		for _, n := range metadataNets {
			if n.Contains(c) {
				return true
			}
		}
	}
	return false
}

// candidates returns ip and the IPv4 addresses it embeds.
func candidates(ip net.IP) []net.IP {
	out := []net.IP{ip}
	if v4 := ip.To4(); v4 != nil {
		return append(out, v4)
	}
	b := ip.To16()
	if b == nil {
		return out
	}
	switch {
	case nat64WellKnown.Contains(b):
		out = append(out, net.IP(append([]byte(nil), b[12:16]...)))
	case nat64LocalUse.Contains(b):
		// RFC 6052, /48 prefix: the IPv4 address occupies bytes 6-7 and 9-10
		// (byte 8 is the reserved u-octet).
		out = append(out, net.IP{b[6], b[7], b[9], b[10]})
	case sixToFour.Contains(b):
		out = append(out, net.IP(append([]byte(nil), b[2:6]...)))
	case teredo.Contains(b):
		out = append(out, net.IP(append([]byte(nil), b[4:8]...))) // the Teredo server
		out = append(out, net.IP{^b[12], ^b[13], ^b[14], ^b[15]}) // the obfuscated client
	}
	// IPv4-compatible ::a.b.c.d (deprecated): first 12 bytes zero.
	zero := true
	for _, x := range b[:12] {
		if x != 0 {
			zero = false
		}
	}
	if zero {
		out = append(out, net.IP(append([]byte(nil), b[12:16]...)))
	}
	return out
}

// ParseIPLoose parses an IP address the way C's inet_aton does, in addition to
// the standard forms: a single decimal, octal (leading 0) or hex (0x) number
// (2852039166, 0xa9fea9fe, 025177524776), or 2 to 4 dot-separated parts of those
// where the last part fills the remaining bytes (169.254.43518, 0251.0376.0251.0376).
// A library that hands the name to getaddrinfo dials those addresses; checking
// the parsed bytes closes that way around a textual check. It returns nil if s
// is not an address.
func ParseIPLoose(s string) net.IP {
	s = strings.TrimSpace(s)
	if ip := net.ParseIP(s); ip != nil {
		return ip
	}
	if s == "" || strings.ContainsAny(s, ":%") {
		return nil
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return nil
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil
		}
		base, digits := 10, p
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			base, digits = 16, p[2:]
			if digits == "" {
				return nil
			}
		case len(p) > 1 && p[0] == '0':
			base, digits = 8, p[1:]
		}
		n, err := strconv.ParseUint(digits, base, 64)
		if err != nil {
			return nil
		}
		nums[i] = n
	}
	var v uint64
	last := nums[len(nums)-1]
	for i, n := range nums[:len(nums)-1] {
		if n > 255 {
			return nil
		}
		v |= n << (24 - 8*uint(i))
	}
	remaining := 8 * uint(4-(len(nums)-1)) // bits left for the last part
	if remaining < 64 && last >= 1<<remaining {
		return nil
	}
	v |= last
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) //nolint:gosec // v is below 2^32 here
}
