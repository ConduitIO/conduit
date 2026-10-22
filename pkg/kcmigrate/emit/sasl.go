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

package emit

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
)

// saslSettings are the builtin:kafka settings that carry SASL authentication.
const (
	settingSASLMechanism = "saslMechanism"
	settingSASLUsername  = "saslUsername"
	settingSASLPassword  = "saslPassword"
	tlsOn                = "true"
	protocolSSL          = "SSL"
	protocolSASLSSL      = "SASL_SSL"
)

var saslSettings = []string{settingSASLMechanism, settingSASLUsername, settingSASLPassword}

// DropUnprotectedSASL removes the SASL settings from every Kafka side of the
// plan that Kafka Connect would not have authenticated with SASL over TLS, and
// turns the report entries that wrote them into blocking "dropped" entries.
//
// Kafka Connect applies sasl.* only when security.protocol is SASL_SSL or
// SASL_PLAINTEXT. With PLAINTEXT, with SSL, or with no security.protocol at all
// the client ignores them, so writing them would authenticate where the
// running connector did not. And a SASL setup without TLS sends the
// credentials in the clear and is refused by the Kafka connector, so migrate
// never writes SASL credentials with TLS off: SASL_PLAINTEXT is blocked on its
// own key, and the rest is dropped here. The entry lists are updated in place.
func DropUnprotectedSASL(p *Plan, entryLists ...[]kcmigrate.Entry) {
	dropped := map[string]bool{} // config path -> true
	droppedEnv := map[string]bool{}
	for i := range p.connectors {
		c := &p.connectors[i]
		tlsPath := fmt.Sprintf("/pipelines/0/connectors/%d/settings/tls.enabled", i)
		if o, ok := p.owner[tlsPath]; ok && c.Settings["tls.enabled"] == tlsOn && p.protocol[o.keyRef] == protocolSASLSSL {
			continue
		}
		for _, name := range saslSettings {
			v, ok := c.Settings[name]
			if !ok {
				continue
			}
			path := fmt.Sprintf("/pipelines/0/connectors/%d/settings/%s", i, escapePointer(name))
			dropped[path] = true
			if m := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}"); m != v {
				droppedEnv[m] = true
			}
			delete(c.Settings, name)
			delete(p.owner, path)
		}
	}
	if len(dropped) == 0 {
		return
	}
	// A variable that a setting we keep still references (the same
	// ${env:NAME} used on both sides, one of them with SASL over TLS) stays in
	// the secrets template and is not the dropped entries' variable.
	for i := range p.connectors {
		for _, v := range p.connectors[i].Settings {
			if m := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}"); m != v {
				delete(droppedEnv, m)
			}
		}
	}
	kept := p.secrets[:0]
	for _, s := range p.secrets {
		if !droppedEnv[s.Env] {
			kept = append(kept, s)
		}
	}
	p.secrets = kept

	for _, list := range entryLists {
		for i := range list {
			e := &list[i]
			if e.Target == nil || (!dropped[e.Target.ConfigPath] && !droppedEnv[e.Target.Env]) {
				continue
			}
			*e = kcmigrate.Entry{
				Key: e.Key, Status: kcmigrate.StatusUnsupported, Origin: e.Origin, Blocking: true,
				Code:   kcmigrate.CodeKeyUnsupported.Reason(),
				Reason: "Dropped: migrate writes SASL settings only for security.protocol SASL_SSL. Kafka Connect ignores them with PLAINTEXT, with SSL and when security.protocol is not set, so the running connector does not authenticate with SASL there; with SASL_PLAINTEXT it does, but over a connection without TLS, which migrate does not write (the credentials would be sent in the clear and the Kafka connector refuses that).",
				Fix:    "If the cluster needs no SASL, remove the sasl.* keys from the Kafka Connect config. If it does, enable TLS on the cluster and set security.protocol to SASL_SSL, then re-run.",
			}
		}
	}
}
