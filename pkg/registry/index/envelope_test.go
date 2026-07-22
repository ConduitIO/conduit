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

package index_test

import (
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/registry/index"
)

// FuzzParseEnvelope is the P0-2 native Go fuzz target for the envelope
// parser (plan-v2 §2.4 item 3): ParseUnverified must never panic on
// arbitrary bytes, whether or not they happen to be valid JSON, a valid
// envelope, or a valid schemaVersion-1 payload.
func FuzzParseEnvelope(f *testing.F) {
	sample, err := os.ReadFile("testdata/sample-index.json")
	if err == nil {
		f.Add(sample)
	}
	f.Add([]byte(`{"payload": {"schemaVersion": 1, "index": {"version": 1, "timestamp": "2026-01-01T00:00:00Z"}, "connectors": []}, "signatures": []}`))
	f.Add([]byte(`{"payload": {"schemaVersion": 999999, "index": {}, "connectors": []}, "signatures": []}`))
	f.Add([]byte(`{"payload": {}, "signatures": [], "payload": {}}`)) // duplicate top-level key
	f.Add([]byte(strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat("}", 200)))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"payload": "not an object", "signatures": []}`))
	f.Add([]byte(`{"payload": {"schemaVersion": "not a number"}, "signatures": []}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// The only invariant under fuzzing: never panic, regardless of
		// whether ParseUnverified accepts or refuses this input.
		_, _ = index.ParseUnverified(data)
	})
}
