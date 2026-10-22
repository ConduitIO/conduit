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
	"net"
	"net/url"
	"strconv"
	"strings"

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
}

// egressRefs lists the network targets named by the settings of a built-in
// connector, the same way fileRefs lists files:
//
//   - postgres: the host(s) and port(s) of the connection string `url`, parsed by
//     pgx itself so the check sees what pgx will dial (including fallbacks and
//     PG* environment defaults);
//   - kafka: the brokers in `servers`;
//   - s3: `aws.endpoint`, or the regional endpoint when it is empty.
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
	if ep == "" {
		region := strings.TrimSpace(settings["aws.region"])
		host := "s3.amazonaws.com"
		if region != "" {
			host = "s3." + region + ".amazonaws.com"
		}
		ref.targets = []connegress.Target{{Host: host, Port: "443"}}
		return ref
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
