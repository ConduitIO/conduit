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
	"net/url"
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/dialer"
	"github.com/conduitio/conduit/pkg/foundation/connegress"
	"github.com/jackc/pgx/v5/pgconn"
)

// Settings and plugin names of the connectors that connect somewhere.
const (
	kafkaServersSetting = "servers"
	s3EndpointSetting   = "aws.endpoint"
	pluginKafka         = "kafka"
	pluginS3            = "s3"
)

// egressRef is one setting that makes a built-in connector connect somewhere.
type egressRef struct {
	// key is the setting that holds the target.
	key string
	// targets are the places it connects to (more than one for a list of Kafka
	// brokers or Postgres hosts).
	targets []connegress.Target
	// missingHost is set when the setting names no host at all (a Postgres
	// connection string that relies on the local socket or PGHOST): with an
	// allowlist the target cannot be vetted.
	missingHost bool
	// unvettable, when set, is why the targets cannot be known from the
	// settings (the client library chooses them): with an allowlist the setting
	// is refused.
	unvettable string
}

// egressRefs lists the network targets named by the settings of a built-in
// connector, the same way fileRefs lists files:
//
//   - postgres: the host(s) and port(s) of the connection string `url`, parsed by
//     pgx itself so the check sees what pgx will dial (including fallbacks and
//     PG* environment defaults);
//   - kafka: the brokers in `servers`;
//   - s3: `aws.endpoint` (only vettable with aws.pathStyle=true; otherwise the
//     client chooses the host and, under an allowlist, the setting is refused).
//
// The generator, file and log connectors do not connect anywhere. A test fails
// when a built-in connector gains a parameter that looks like an address and is
// not covered here or in the reviewed list.
func egressRefs(pluginName string, settings map[string]string) []egressRef {
	switch pluginBase(pluginName) {
	case pluginPostgres:
		if u, ok := settings[postgresURLSetting]; ok && strings.TrimSpace(u) != "" {
			return []egressRef{postgresEgress(u)}
		}
	case pluginKafka:
		if s, ok := settings[kafkaServersSetting]; ok {
			return []egressRef{kafkaEgress(s)}
		}
	case pluginS3:
		return []egressRef{s3Egress(settings)}
	}
	return nil
}

func postgresEgress(conn string) egressRef {
	ref := egressRef{key: postgresURLSetting}
	// The ambient files are blanked first, as ResolveSettings does for the
	// connector, so parsing never reads them.
	cfg, err := pgconn.ParseConfig(withEmptyAmbientFiles(conn))
	if err != nil {
		return ref // the connector fails on it too; nothing is dialed
	}
	add := func(host string, port uint16) {
		if host == "" {
			ref.missingHost = true
			return
		}
		t := connegress.Target{Host: host, Port: strconv.Itoa(int(port))}
		for _, have := range ref.targets { // pgx lists a TLS and a plain fallback per host
			if have == t {
				return
			}
		}
		ref.targets = append(ref.targets, t)
	}
	add(cfg.Host, cfg.Port)
	for _, fb := range cfg.Fallbacks {
		add(fb.Host, fb.Port)
	}
	return ref
}

func kafkaEgress(servers string) egressRef {
	ref := egressRef{key: kafkaServersSetting}
	for _, s := range strings.Split(servers, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			host, port = strings.Trim(s, "[]"), "9092"
		}
		ref.targets = append(ref.targets, connegress.Target{Host: host, Port: port})
	}
	return ref
}

func s3Egress(settings map[string]string) egressRef {
	ref := egressRef{key: s3EndpointSetting}
	ep := strings.TrimSpace(settings[s3EndpointSetting])
	// The AWS SDK, not this code, decides where it connects: the regional
	// endpoint or its FIPS and dual-stack variants, other partitions (cn, gov),
	// AWS_ENDPOINT_URL* variables, and, in virtual-hosted style, a subdomain
	// named after the bucket. Guessing any of that here would let the check
	// approve one host while the SDK dials another. So an endpoint is only
	// vettable when it is given explicitly and the bucket stays in the path.
	if ep == "" {
		ref.unvettable = "no aws.endpoint is set, so the S3 client chooses the host (regional, FIPS or dual-stack endpoint, bucket subdomain); set aws.endpoint explicitly and aws.pathStyle=true"
		return ref
	}
	if strings.TrimSpace(settings["aws.pathStyle"]) != strconv.FormatBool(true) {
		ref.unvettable = "aws.pathStyle is not true, so the S3 client dials a subdomain named after the bucket; set aws.pathStyle=true"
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Hostname() == "" {
		return ref // the connector fails on it too
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	ref.targets = []connegress.Target{{Host: u.Hostname(), Port: port}}
	return ref
}

// withEgressPolicy is the context decorator that hands a built-in connector the
// operator's connector egress policy and, through the conduit-commons dialer
// seam, the dial guard built from it. A connector that honors the seam opens
// every network connection through p.DialContext, so the policy is applied to
// the address actually dialed, after the name is resolved, and not only to the
// text of a setting when the connector was configured.
//
// A connector release that does not read the seam ignores it and keeps the
// setting check as its only enforcement (see docs/operations/connector-egress.md).
func withEgressPolicy(p *connegress.Policy) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return dialer.WithDialContext(connegress.WithPolicy(ctx, p), p.DialContext)
	}
}
