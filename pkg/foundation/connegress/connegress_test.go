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
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

type stubResolver map[string][]string

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := s[host]
	if !ok {
		return nil, cerrors.New("no such host")
	}
	var out []net.IPAddr
	for _, i := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(i)})
	}
	return out, nil
}

func TestParseIPLoose(t *testing.T) {
	meta := net.ParseIP("169.254.169.254")
	for _, s := range []string{
		"169.254.169.254", "2852039166", "0xa9fea9fe", "0XA9FEA9FE", "025177524776",
		"0251.0376.0251.0376", "0xa9.0xfe.0xa9.0xfe", "169.254.43518", "169.16689662", "0xa9.0xfe.0xa9fe",
		"::ffff:169.254.169.254", "::ffff:a9fe:a9fe",
	} {
		ip := ParseIPLoose(s)
		if ip == nil || !ip.Equal(meta) {
			t.Errorf("ParseIPLoose(%q) = %v, want %v", s, ip, meta)
		}
	}
	for _, s := range []string{"", "db.example.com", "1.2.3.4.5", "256.1.1.1", "1.2.3.256", "0x", "1..2", "09.1.1.1", "4294967296", "a.b.c.d", "1.2.3.4:80"} {
		if ip := ParseIPLoose(s); ip != nil {
			t.Errorf("ParseIPLoose(%q) = %v, want nil", s, ip)
		}
	}
}

func TestIsMetadataIP(t *testing.T) {
	yes := []string{
		"169.254.169.254", "169.254.170.2", "169.254.0.1", "168.63.129.16", "100.100.100.200",
		"fd00:ec2::254", "fd00:ec2::1", "fe80::1",
		"::ffff:169.254.169.254",
		"64:ff9b::a9fe:a9fe",             // NAT64
		"64:ff9b:1:a9fe:a9:fe00::",       // NAT64 local-use, /48 embedding (bytes 6,7,9,10)
		"2002:a9fe:a9fe::1",              // 6to4
		"::169.254.169.254",              // IPv4-compatible
		"64:ff9b::a83f:8110",             // NAT64 Azure WireServer 168.63.129.16
		"2001:0:a9fe:a9fe:0:0:5601:5601", // Teredo server 169.254.169.254
	}
	for _, s := range yes {
		if ip := net.ParseIP(s); ip == nil || !IsMetadataIP(ip) {
			t.Errorf("IsMetadataIP(%q) = false", s)
		}
	}
	no := []string{"10.0.0.5", "192.168.1.1", "127.0.0.1", "8.8.8.8", "2606:4700::1111", "::1", "fd12::1", "64:ff9b::808:808", "169.255.0.1", "168.63.129.17"}
	for _, s := range no {
		if ip := net.ParseIP(s); ip == nil || IsMetadataIP(ip) {
			t.Errorf("IsMetadataIP(%q) = true (private and public ranges are not metadata)", s)
		}
	}
}

func TestParseEntries(t *testing.T) {
	es, err := ParseEntries([]string{"db.example.com:5432", "10.1.2.3:9092, 10.0.0.0/8:5432", "[fd00::/8]:5432", "[::1]:*", "*.rds.example.com:5432", "kafka:*"})
	if err != nil || len(es) != 7 {
		t.Fatalf("%v %v", es, err)
	}
	for _, bad := range []string{"db", "db:0", "db:99999", "db:x", ":5432", "[::1]5432", "10.0.0.0/33:5432", "a b:1", "http://x:1"} {
		if _, err := ParseEntries([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if es, err := ParseEntries(nil); err != nil || len(es) != 0 {
		t.Fatal(es, err)
	}
}

func mustPolicy(t *testing.T, r Resolver, entries ...string) *Policy {
	t.Helper()
	es, err := ParseEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	return New(es).WithResolver(r)
}

// check runs the authoritative Check, or the name-only Hint.
func check(p *Policy, host, port string, resolve bool) error {
	t := Target{host, port}
	if resolve {
		return p.Check(context.Background(), "settings.url", t)
	}
	_, err := p.Hint("settings.url", t)
	return err
}

func checkRefused(t *testing.T, p *Policy, host, port string, resolve bool) {
	t.Helper()
	err := check(p, host, port, resolve)
	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != CodeEgressBlocked || ce.ConfigPath != "settings.url" || ce.Suggestion == "" {
		t.Errorf("%s:%s: err = %v, want a coded refusal", host, port, err)
	}
}

func checkAllowed(t *testing.T, p *Policy, host, port string, resolve bool) {
	t.Helper()
	if err := check(p, host, port, resolve); err != nil {
		t.Errorf("%s:%s refused: %v", host, port, err)
	}
}

// Without an allowlist only the metadata refusal applies: databases live in
// private ranges.
func TestCheck_NoAllowlist_MetadataAlwaysRefused(t *testing.T) {
	res := stubResolver{
		"rebinding.example.com": {"169.254.169.254"},
		"mixed.example.com":     {"203.0.113.5", "169.254.169.254"},
		"nat64.example.com":     {"64:ff9b::a9fe:a9fe"},
		"db.internal":           {"10.1.2.3"},
	}
	p := New(nil).WithResolver(res)
	for _, h := range []string{
		"169.254.169.254", "2852039166", "0xa9fea9fe", "0251.0376.0251.0376", "[fd00:ec2::254]", "fd00:ec2::254",
		"::ffff:169.254.169.254", "168.63.129.16", "100.100.100.200", "169.254.170.2", "fe80::1%eth0",
		"metadata.google.internal", "METADATA.GOOGLE.INTERNAL.", "metadata.goog", "instance-data", "instance-data.ec2.internal", "metadata",
	} {
		checkRefused(t, p, h, "80", false)
		checkRefused(t, p, h, "80", true)
	}
	for _, h := range []string{"rebinding.example.com", "mixed.example.com", "nat64.example.com"} {
		checkAllowed(t, p, h, "5432", false) // names are only vetted when resolved
		checkRefused(t, p, h, "5432", true)
	}
	for _, h := range []string{"10.0.0.5", "192.168.1.1", "127.0.0.1", "localhost", "db.internal", "8.8.8.8", "example.com", "unresolvable.example.com"} {
		checkAllowed(t, p, h, "5432", true)
	}
	checkAllowed(t, p, "/var/run/postgresql", "5432", true) // sockets are fine without an allowlist
}

func TestCheck_Allowlist(t *testing.T) {
	res := stubResolver{
		"db.example.com":     {"10.1.2.3"},
		"other.example.com":  {"10.1.2.3"},
		"pg.rds.example.com": {"10.9.9.9"},
		"in-cidr.example":    {"10.7.7.7", "10.7.7.8"},
		"half-cidr.example":  {"10.7.7.7", "192.168.0.1"},
		"meta.example":       {"169.254.169.254"},
		"kafka":              {"172.16.0.9"},
	}
	p := mustPolicy(t, res, "db.example.com:5432", "10.1.2.3:9092", "10.7.0.0/16:5432", "[fd00::/8]:5432", "*.rds.example.com:5432", "kafka:*", "169.254.169.254:80")

	checkAllowed(t, p, "db.example.com", "5432", true)
	checkAllowed(t, p, "DB.EXAMPLE.COM.", "5432", true)
	checkRefused(t, p, "db.example.com", "5433", true)    // wrong port
	checkRefused(t, p, "other.example.com", "5432", true) // not listed, resolves to an unlisted address
	checkAllowed(t, p, "10.1.2.3", "9092", true)
	checkRefused(t, p, "10.1.2.3", "5432", true)
	checkAllowed(t, p, "10.7.200.1", "5432", true)
	checkRefused(t, p, "10.8.0.1", "5432", true)
	checkAllowed(t, p, "fd12::1", "5432", true)
	checkAllowed(t, p, "pg.rds.example.com", "5432", true)
	checkRefused(t, p, "rds.example.com", "5432", true) // the wildcard needs a label
	checkRefused(t, p, "evil-rds.example.com", "5432", true)
	checkAllowed(t, p, "kafka", "1", true)
	checkAllowed(t, p, "kafka", "65535", true)

	// a name that is not listed is allowed when everything it resolves to is
	checkAllowed(t, p, "in-cidr.example", "5432", true)
	checkRefused(t, p, "half-cidr.example", "5432", true)
	// without resolution the name alone can be vetted only if an address entry could cover it
	checkAllowed(t, p, "in-cidr.example", "5432", false)
	checkRefused(t, p, "in-cidr.example", "9999", false)
	checkRefused(t, p, "unknown.example", "7777", false)

	// metadata is refused even when it is listed
	checkRefused(t, p, "169.254.169.254", "80", true)
	checkRefused(t, p, "meta.example", "80", true)

	// unix sockets are not allowed under an allowlist
	checkRefused(t, p, "/var/run/postgresql", "5432", true)
	checkRefused(t, p, "@abstract", "5432", true)
}

func TestCheck_NilPolicy(t *testing.T) {
	var p *Policy
	checkAllowed(t, p, "10.0.0.1", "5432", false)
	checkRefused(t, p, "169.254.169.254", "80", false)
	if p.Restricted() {
		t.Fatal("nil policy is restricted")
	}
}

func TestCheckUnvettable(t *testing.T) {
	addrOnly := mustPolicy(t, stubResolver{}, "10.0.0.0/8:*", "192.168.0.0/16:5432")
	if err := addrOnly.CheckUnvettable("settings.url", "no host"); err == nil {
		t.Fatal("an unvettable target was allowed under an allowlist")
	}
	if err := New(nil).CheckUnvettable("settings.url", "no host"); err != nil {
		t.Fatalf("no allowlist, nothing to enforce: %v", err)
	}
	var nilPolicy *Policy
	if err := nilPolicy.CheckUnvettable("k", "x"); err != nil {
		t.Fatal(err)
	}
	// an empty host through Check is unvettable too, even though entries with a
	// wildcard port would "cover" any name
	for _, tt := range []Target{{"", "0"}, {"", "5432"}} {
		if err := addrOnly.Check(context.Background(), "settings.url", tt); err == nil {
			t.Errorf("empty host %v allowed", tt)
		}
		if _, err := addrOnly.Hint("settings.url", tt); err == nil {
			t.Errorf("empty host %v allowed by Hint", tt)
		}
	}
}

// A hint can defer; a deferral is never permission.
func TestHint_DeferralIsNotPermission(t *testing.T) {
	res := stubResolver{"db.test": {"172.16.5.5"}}
	p := mustPolicy(t, res, "10.0.0.0/8:5432")

	deferred, err := p.Hint("settings.url", Target{"db.test", "5432"})
	if err != nil || !deferred {
		t.Fatalf("Hint = %v, %v; want a deferral", deferred, err)
	}
	// the authoritative check, and a dial, both refuse it
	checkRefused(t, p, "db.test", "5432", true)
	if c, err := p.DialContext(context.Background(), "tcp", "db.test:5432"); err == nil {
		c.Close()
		t.Fatal("a deferred target connected without being re-checked")
	}
	// a name no entry could cover is refused by the hint already
	if _, err := p.Hint("settings.url", Target{"db.test", "9999"}); err == nil {
		t.Fatal("hint allowed a name no entry covers")
	}
	// unrestricted: the hint still defers the address check, and says so
	if deferred, err := New(nil).Hint("settings.url", Target{"db.test", "1"}); err != nil || !deferred {
		t.Fatalf("unrestricted Hint = %v, %v", deferred, err)
	}
}

func listener(t *testing.T) (port string, accepted chan struct{}) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen:", err)
	}
	accepted = make(chan struct{}, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	_, port, _ = net.SplitHostPort(l.Addr().String())
	return port, accepted
}

// flipDNS answers with its current answer, which a test changes.
type flipDNS struct{ ips []string }

func (f *flipDNS) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, i := range f.ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(i)})
	}
	return out, nil
}

// Every dial hook the built-in connectors' client libraries have goes through the
// same guard: a resolved address that is not allowed is not connected to.
func TestDialGuard_PerClientAdapter(t *testing.T) {
	port, accepted := listener(t)
	res := stubResolver{"db.test": {"127.0.0.1"}}
	outside := mustPolicy(t, res, "10.0.0.0/8:*", "db.other:5432") // 127.0.0.1 is not listed
	inside := mustPolicy(t, res, "127.0.0.0/8:*")

	dials := map[string]func(p *Policy, addr string) (net.Conn, error){
		"postgres (pgx DialFunc)": func(p *Policy, addr string) (net.Conn, error) {
			return p.PGXDialFunc()(context.Background(), "tcp", addr)
		},
		"kafka (franz-go Dialer)": func(p *Policy, addr string) (net.Conn, error) {
			return p.KafkaDialer()(context.Background(), "tcp", addr)
		},
		"mysql (driver DialContext)": func(p *Policy, addr string) (net.Conn, error) {
			return p.MySQLDialContext("tcp")(context.Background(), addr)
		},
		"s3 (http.Transport)": func(p *Policy, addr string) (net.Conn, error) {
			return p.HTTPTransport().DialContext(context.Background(), "tcp", addr)
		},
	}
	for name, dial := range dials {
		for _, addr := range []string{"127.0.0.1:" + port, "db.test:" + port, "localhost:" + port} {
			if c, err := dial(outside, addr); err == nil {
				c.Close()
				t.Errorf("%s: %s connected although its resolved address is not allowed", name, addr)
			}
		}
		if c, err := dial(inside, "127.0.0.1:"+port); err != nil {
			t.Errorf("%s: allowed dial failed: %v", name, err)
		} else {
			c.Close()
		}
		if c, err := dial(inside, "db.test:"+port); err != nil {
			t.Errorf("%s: allowed name failed: %v", name, err)
		} else {
			c.Close()
		}
	}
	// the refusals never reached the listener
	select {
	case <-accepted:
	default:
		t.Fatal("setup: allowed dials did not arrive")
	}
}

func TestDialGuard_HTTPClient(t *testing.T) {
	port, _ := listener(t)
	p := mustPolicy(t, stubResolver{}, "10.0.0.0/8:*")
	client := &http.Client{Transport: p.HTTPTransport(), Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+port+"/", http.NoBody)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the HTTP client connected to an address outside the allowlist")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("not a policy refusal: %v", err)
	}
}

// What the name resolves to at the dial is what counts, not what it resolved to
// when the setting was checked (DNS rebinding), and the connection goes to the
// vetted address.
func TestDialGuard_RebindingAndUnvettable(t *testing.T) {
	port, _ := listener(t)
	dns := &flipDNS{ips: []string{"127.0.0.1"}}
	p := mustPolicy(t, dns, "127.0.0.0/8:*")

	checkAllowed(t, p, "db.test", port, true)
	dns.ips = []string{"169.254.169.254"}
	if c, err := p.DialContext(context.Background(), "tcp", "db.test:"+port); err == nil {
		c.Close()
		t.Fatal("rebinding to the metadata address connected")
	}
	dns.ips = []string{"127.0.0.1", "169.254.169.254"}
	if c, err := p.DialContext(context.Background(), "tcp", "db.test:"+port); err == nil {
		c.Close()
		t.Fatal("a metadata address among the answers connected")
	}

	// cannot be vetted: no host, no port, unresolvable, a socket
	for _, addr := range []string{":" + port, "noport", ""} {
		if c, err := p.DialContext(context.Background(), "tcp", addr); err == nil {
			c.Close()
			t.Errorf("%q connected", addr)
		}
	}
	if c, err := mustPolicy(t, stubResolver{}, "127.0.0.0/8:*").DialContext(context.Background(), "tcp", "unresolvable.test:"+port); err == nil {
		c.Close()
		t.Error("an unresolvable name connected")
	}
	if c, err := p.DialContext(context.Background(), "unix", "/var/run/postgresql/.s.PGSQL.5432"); err == nil {
		c.Close()
		t.Error("a unix socket connected under an allowlist")
	}
	// metadata is refused for an unrestricted policy too
	if c, err := New(nil).WithResolver(dns).DialContext(context.Background(), "tcp", "db.test:"+port); err == nil {
		c.Close()
		t.Error("unrestricted policy connected to a metadata answer")
	}
}
