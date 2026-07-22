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

package toproto

import "github.com/conduitio/conduit/pkg/foundation/log"

// redactSettings returns a copy of in with every value replaced by
// log.Redacted ("***"); keys are preserved so callers can still see which
// settings exist. It never mutates in.
//
// Security: connector, processor and DLQ Settings/Config maps routinely
// contain credentials — database passwords, SASL/SCRAM secrets, cloud access
// keys — entered by users via config-as-code or the API's own Create/Update
// RPCs. conduit-commons' config.Parameter has no field marking a parameter as
// secret yet (tracked upstream as conduit-commons#2566), so there is no
// reliable way to identify *which* keys in a given map are sensitive from
// this package. Rather than guess with a key-name heuristic (which leaks
// exactly the key it fails to match), every outbound Settings/Config value is
// redacted: deny-by-default. This mirrors the log-output redaction already
// done for connector settings (see pkg/foundation/log/redact.go) — that path
// covers what gets written to logs, this one covers what the HTTP/gRPC API
// hands back to a caller. The API is unauthenticated by default, so this is
// the last line of defense against a secret leaking to anyone who can reach
// it.
//
// This must be called at every toproto conversion that embeds a
// Settings/Config map in a response (see ConnectorConfig, ProcessorConfig,
// PipelineDLQ) — a single missed call site reintroduces the leak. It must
// NOT be called anywhere on the inbound (fromproto) path: Create/Update
// requests need the real values to configure the connector/processor, and
// config-as-code provisioning parses YAML directly, never through this
// package.
func redactSettings(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k := range in {
		out[k] = log.Redacted
	}
	return out
}
