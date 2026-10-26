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

package conduit

import (
	"context"
	"net"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/connegress"
	"github.com/matryer/is"
)

type mapDNS map[string][]string

func (m mapDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := m[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []net.IPAddr
	for _, i := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(i)})
	}
	return out, nil
}

func targets(refs []egressRef) []string {
	var out []string
	for _, r := range refs {
		for _, t := range r.targets {
			out = append(out, r.key+"="+t.String())
		}
		if r.missingHost {
			out = append(out, r.key+"=(no host)")
		}
		if r.unvettable != "" {
			out = append(out, r.key+"=(unvettable)")
		}
	}
	return out
}

func TestEgressRefs_Extraction(t *testing.T) {
	is := is.New(t)
	t.Setenv("PGHOST", "")
	cases := []struct {
		plugin   string
		settings map[string]string
		want     []string
	}{
		{"builtin:postgres", map[string]string{"url": "postgres://u:p@db.example.com:6432/app"}, []string{"url=db.example.com:6432"}},
		{"postgres", map[string]string{"url": "postgres://u@db.example.com/app?sslmode=disable"}, []string{"url=db.example.com:5432"}},
		{"builtin:postgres@v0.14.5", map[string]string{"url": "host=a.example.com,b.example.com port=5433,5434 user=u"}, []string{"url=a.example.com:5433", "url=b.example.com:5434"}},
		{"builtin:postgres", map[string]string{"url": "postgres://u@[fd00::5]:5432/app"}, []string{"url=[fd00::5]:5432"}},
		{"builtin:postgres", map[string]string{"url": "postgres://u@10.0.0.1:5432,10.0.0.2:5433/app"}, []string{"url=10.0.0.1:5432", "url=10.0.0.2:5433"}},
		{"builtin:postgres", map[string]string{"url": "postgres://u@/app?host=/var/run/postgresql"}, []string{"url=/var/run/postgresql:5432"}},
		{"builtin:kafka", map[string]string{"servers": "k1.example.com:9093, k2.example.com ,[fd00::9]:9092"}, []string{"servers=k1.example.com:9093", "servers=k2.example.com:9092", "servers=[fd00::9]:9092"}},
		{"builtin:s3", map[string]string{"aws.region": "eu-west-1", "aws.bucket": "b"}, []string{"aws.endpoint=(unvettable)"}},
		{"builtin:s3", map[string]string{"aws.endpoint": "http://minio.internal:9000", "aws.pathStyle": "true"}, []string{"aws.endpoint=minio.internal:9000"}},
		{"builtin:s3", map[string]string{"aws.endpoint": "http://minio.internal:9000"}, []string{"aws.endpoint=minio.internal:9000", "aws.endpoint=(unvettable)"}},
		{"builtin:s3", map[string]string{"aws.endpoint": "https://s3.example.com", "aws.pathStyle": "true"}, []string{"aws.endpoint=s3.example.com:443"}},
		{"builtin:s3", map[string]string{"aws.endpoint": "minio.internal:9000", "aws.pathStyle": "true"}, []string{"aws.endpoint=minio.internal:9000"}},
		{"builtin:file", map[string]string{"path": "/x"}, nil},
		{"builtin:generator", map[string]string{"rate": "5"}, nil},
		{"builtin:log", map[string]string{"level": "debug"}, nil},
		{"builtin:postgres", map[string]string{"url": "not a url"}, nil},
	}
	for _, c := range cases {
		is.Equal(targets(egressRefs(c.plugin, c.settings)), c.want)
	}
}

func egressFixture(t *testing.T, dns mapDNS, allow ...string) fsFixture {
	t.Helper()
	f := newFSFixture(t)
	es, err := connegress.ParseEntries(allow)
	if err != nil {
		t.Fatal(err)
	}
	f.r.egress = connegress.New(es).WithResolver(dns)
	return f
}

func (f fsFixture) refusesEgress(t *testing.T, plugin string, settings map[string]string, wantKey string) {
	t.Helper()
	_, err := f.r.ResolveSettings(plugin, settings)
	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != connegress.CodeEgressBlocked || ce.ConfigPath != "settings."+wantKey {
		t.Fatalf("%s %v: err = %v, want %s at settings.%s", plugin, settings, err, connegress.CodeEgressBlocked.Reason(), wantKey)
	}
}

// Metadata endpoints are refused for every connector, in every encoding, as a
// name, and as what a name resolves to, with or without an allowlist.
func TestConnectorEgress_MetadataAlwaysRefused(t *testing.T) {
	dns := mapDNS{"rebind.example.com": {"169.254.169.254"}, "nat64.example.com": {"64:ff9b::a9fe:a9fe"}}
	for _, f := range []fsFixture{egressFixture(t, dns), egressFixture(t, dns, "rebind.example.com:5432", "169.254.169.254:80", "0.0.0.0/0:*")} {
		for _, h := range []string{
			"169.254.169.254", "2852039166", "0xa9fea9fe", "0251.0376.0251.0376", "[fd00:ec2::254]", "metadata.google.internal",
			"168.63.129.16", "[::ffff:169.254.169.254]", "[64:ff9b::a9fe:a9fe]", "rebind.example.com", "nat64.example.com",
		} {
			f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres://u:p@" + h + ":5432/app"}, "url")
			f.refusesEgress(t, "builtin:kafka", map[string]string{"servers": "ok.example.com:9092," + h + ":9092"}, "servers")
			f.refusesEgress(t, "builtin:s3", map[string]string{"aws.endpoint": "http://" + h + "/", "aws.bucket": "b"}, "aws.endpoint")
		}
		// the AWS metadata service is also what an s3 "region" must not turn into
		f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "host=db.example.com,169.254.169.254 user=u"}, "url")
	}
}

func TestConnectorEgress_NoAllowlistKeepsPrivateTargets(t *testing.T) {
	f := egressFixture(t, mapDNS{"db.internal": {"10.1.2.3"}})
	for plugin, s := range map[string]map[string]string{
		"builtin:postgres": {"url": "postgres://u:p@db.internal:5432/app"},
		"builtin:kafka":    {"servers": "10.0.0.5:9092,127.0.0.1:9092"},
		"builtin:s3":       {"aws.endpoint": "http://localhost:9000", "aws.bucket": "b"},
	} {
		if _, err := f.r.ResolveSettings(plugin, s); err != nil {
			t.Errorf("%s %v refused: %v", plugin, s, err)
		}
	}
	// local socket / PGHOST: allowed when there is no allowlist
	if _, err := f.r.ResolveSettings("builtin:postgres", map[string]string{"url": "postgres:///app"}); err != nil {
		t.Errorf("socket connection refused: %v", err)
	}
}

func TestConnectorEgress_Allowlist(t *testing.T) {
	dns := mapDNS{"db.example.com": {"10.1.2.3"}, "evil.example.com": {"10.9.9.9"}, "k1.example.com": {"10.5.0.1"}}
	f := egressFixture(t, dns, "db.example.com:5432", "10.5.0.0/16:9092", "s3.eu-west-1.amazonaws.com:443", "minio.internal:9000")

	ok := []struct {
		plugin string
		s      map[string]string
	}{
		{"builtin:postgres", map[string]string{"url": "postgres://u@db.example.com:5432/app"}},
		{"builtin:postgres", map[string]string{"url": "postgres://u@db.example.com/app"}},
		{"builtin:kafka", map[string]string{"servers": "10.5.0.7:9092,k1.example.com:9092"}},
		{"builtin:s3", map[string]string{"aws.endpoint": "http://minio.internal:9000", "aws.pathStyle": "true"}},
		{"builtin:file", map[string]string{"path": f.files + "/x"}},
		{"builtin:log", map[string]string{"level": "debug"}},
	}
	for _, c := range ok {
		if _, err := f.r.ResolveSettings(c.plugin, c.s); err != nil {
			t.Errorf("%s %v refused: %v", c.plugin, c.s, err)
		}
	}
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres://u@db.example.com:5433/app"}, "url")
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres://u@evil.example.com:5432/app"}, "url")
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "host=db.example.com,evil.example.com"}, "url")
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres://u@10.1.2.3:5432/app"}, "url") // the address is not listed, only the name
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres:///app"}, "url")                // socket / PGHOST cannot be vetted
	f.refusesEgress(t, "builtin:kafka", map[string]string{"servers": "10.5.0.7:9092,10.6.0.1:9092"}, "servers")
	f.refusesEgress(t, "builtin:s3", map[string]string{"aws.endpoint": "https://attacker.example.net", "aws.pathStyle": "true"}, "aws.endpoint")
	// the S3 client chooses the host (region, FIPS, dual-stack, bucket subdomain):
	// not vettable from the settings, so refused under an allowlist even when the
	// guessed regional host is listed
	f.refusesEgress(t, "builtin:s3", map[string]string{"aws.region": "eu-west-1"}, "aws.endpoint")
	f.refusesEgress(t, "builtin:s3", map[string]string{"aws.region": "eu-west-1", "aws.endpoint": "http://minio.internal:9000"}, "aws.endpoint") // virtual-hosted style

	// the validate path checks names only (no lookups) and skips references
	if err := f.r.CheckSettings("builtin:kafka", map[string]string{"servers": "10.6.0.1:9092"}); err == nil {
		t.Error("validate request accepted a target outside the allowlist")
	}
	if err := f.r.CheckSettings("builtin:kafka", map[string]string{"servers": "${BROKER}:9092"}); err != nil {
		t.Errorf("reference refused by the validate path: %v", err)
	}
	if err := f.r.CheckSettings("builtin:postgres", map[string]string{"url": "postgres://u@169.254.169.254/app"}); err == nil {
		t.Error("validate request accepted the metadata endpoint")
	}
}

func TestConnectorEgress_ConfigIsValidatedAtStartup(t *testing.T) {
	cfg := DefaultConfigWithBasePath(t.TempDir())
	cfg.Connectors.Egress.Allow = []string{"db.example.com:5432", "10.0.0.0/8:9092"}
	if err := cfg.validateHardeningConfig(); err != nil {
		t.Fatal(err)
	}
	cfg.Connectors.Egress.Allow = []string{"db.example.com"}
	err := cfg.validateHardeningConfig()
	if err == nil || !strings.Contains(err.Error(), connegress.AllowConfigPath) {
		t.Fatalf("err = %v", err)
	}
}

// TestBuiltinConnectorNetworkParams fails when a built-in connector gains a
// parameter that looks like it names a network address and is not covered by
// egressRefs. It keeps the table of connection targets from going stale when a
// connector is bumped.
func TestBuiltinConnectorNetworkParams(t *testing.T) {
	looksLikeAddress := regexp.MustCompile(`(?i)(host|server|endpoint|url|addr|broker|uri|dsn|conn)`)
	covered := map[string]map[string]struct{}{
		"postgres": {"url": {}},
		"kafka":    {"servers": {}},
		"s3":       {"aws.endpoint": {}},
	}
	for module, c := range builtin.DefaultBuiltinConnectors {
		name := strings.TrimPrefix(path.Base(module), "conduit-connector-")
		spec := c.NewSpecification()
		check := func(kind, key string) {
			if strings.HasPrefix(key, "sdk.") || !looksLikeAddress.MatchString(key) {
				return
			}
			if key == "aws.serverSideEncryption" { // s3: encryption mode, not an address
				return
			}
			if _, ok := covered[name][key]; !ok {
				t.Errorf("%s %s parameter %q looks like it names an address: add it to egressRefs (and the covered list here)", name, kind, key)
			}
		}
		for key := range spec.SourceParams {
			check("source", key)
		}
		for key := range spec.DestinationParams {
			check("destination", key)
		}
	}
}

// An allowlist with address entries only (a wildcard port among them) used to
// "cover" any name at the hint stage, so a connection string with no host failed
// open. It is refused, with and without PGHOST, and the validate path agrees.
func TestConnectorEgress_NoHostWithAddressOnlyAllowlist(t *testing.T) {
	f := egressFixture(t, mapDNS{"pg.example.com": {"10.9.9.9"}}, "10.0.0.0/8:*", "192.168.0.0/16:5432")

	t.Setenv("PGHOST", "")
	for _, u := range []string{"postgres:///app", "user=u dbname=app", "postgres://u@/app?sslmode=disable"} {
		f.refusesEgress(t, "builtin:postgres", map[string]string{"url": u}, "url")
		if err := f.r.CheckSettings("builtin:postgres", map[string]string{"url": u}); err == nil {
			t.Errorf("validate path accepted %q", u)
		}
	}

	// PGHOST names the host: it is a target like any other and must be listed
	t.Setenv("PGHOST", "pg.example.com") // resolves inside 10.0.0.0/8: allowed
	if _, err := f.r.ResolveSettings("builtin:postgres", map[string]string{"url": "postgres:///app"}); err != nil {
		t.Errorf("PGHOST inside the allowlist refused: %v", err)
	}
	t.Setenv("PGHOST", "elsewhere.example.org") // unresolvable and unlisted
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres:///app"}, "url")
	t.Setenv("PGHOST", "169.254.169.254")
	f.refusesEgress(t, "builtin:postgres", map[string]string{"url": "postgres:///app"}, "url")

	// no allowlist: a socket connection is fine
	g := egressFixture(t, nil)
	t.Setenv("PGHOST", "")
	if _, err := g.r.ResolveSettings("builtin:postgres", map[string]string{"url": "postgres:///app"}); err != nil {
		t.Errorf("socket refused without an allowlist: %v", err)
	}
}

// The validate path takes hints only: nothing it lets through is permission to
// connect, and a deferred name is refused by the configure-time check.
func TestConnectorEgress_ValidateIsOnlyAHint(t *testing.T) {
	f := egressFixture(t, mapDNS{"db.example.com": {"172.16.0.9"}}, "10.0.0.0/8:5432")
	s := map[string]string{"url": "postgres://u@db.example.com:5432/app"}
	if err := f.r.CheckSettings("builtin:postgres", s); err != nil {
		t.Fatalf("hint should defer a name an address entry could cover: %v", err)
	}
	f.refusesEgress(t, "builtin:postgres", s, "url") // resolved: outside the allowlist
}

// The context decorator hands the policy to the connectors that connect.
func TestEgressPolicyRidesOnTheConnectorContext(t *testing.T) {
	p := connegress.New(nil)
	ctx := withEgressPolicy(p)(context.Background())
	if connegress.PolicyFrom(ctx) != p {
		t.Fatal("the policy is not on the context")
	}
}
