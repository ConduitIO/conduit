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

package api

// wantRedactedSettings is the shared test expectation for what an outbound
// Connector/Processor/DLQ Settings map looks like once it has crossed the API
// boundary (#2640): every value replaced with "***", every key preserved.
//
// It is a separate, hand-rolled implementation rather than a call into
// toproto's own (unexported) redaction helper on purpose — asserting against
// the production implementation would make the test tautological (it would
// pass even if that implementation regressed to a no-op). Keep this in sync
// with the "***" constant (github.com/conduitio/conduit/pkg/foundation/log.Redacted)
// if that ever changes.
func wantRedactedSettings(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k := range in {
		out[k] = "***"
	}
	return out
}
