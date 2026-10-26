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

package pipeline

import "testing"

// TestDLQ_LogsWithoutPayload is the regression test for GHSA-m3mw-fpw8-w765's
// DLQ half: the default DLQ (builtin:log without payload:true) preserves only
// hashes of a nacked record, so it must be refused at start. Only an explicit
// payload:true (any casing, any surrounding space) or a plugin other than the
// log connector makes the DLQ recoverable.
func TestDLQ_LogsWithoutPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    DLQ
		want bool
	}{
		{name: "default DLQ", d: DefaultDLQ, want: true},
		{name: "log without settings", d: DLQ{Plugin: "builtin:log"}, want: true},
		{name: "log with payload false", d: DLQ{Plugin: "builtin:log", Settings: map[string]string{"payload": "false"}}, want: true},
		{name: "log with payload empty", d: DLQ{Plugin: "builtin:log", Settings: map[string]string{"payload": ""}}, want: true},
		{name: "log with payload true", d: DLQ{Plugin: "builtin:log", Settings: map[string]string{"payload": "true"}}, want: false},
		{name: "log with payload TRUE", d: DLQ{Plugin: "builtin:log", Settings: map[string]string{"payload": "TRUE"}}, want: false},
		{name: "log with payload spaced", d: DLQ{Plugin: "builtin:log", Settings: map[string]string{"payload": " true "}}, want: false},
		{name: "non-log plugin", d: DLQ{Plugin: "builtin:kafka"}, want: false},
		{name: "standalone log plugin name", d: DLQ{Plugin: "builtin:file"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.LogsWithoutPayload(); got != tt.want {
				t.Fatalf("LogsWithoutPayload() = %v, want %v", got, tt.want)
			}
		})
	}
}
