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
// DLQ half: builtin:log without payload:true preserves only hashes of a nacked
// record. Only an explicit payload:true (any casing, any surrounding space) or
// a plugin other than the log connector preserves the content. Whether a start
// is refused also depends on whether nacks are routed (DropsRecordContent).
func TestDLQ_LogsWithoutPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    DLQ
		want bool
	}{
		{name: "default DLQ", d: DefaultDLQ, want: true},
		{name: "log without settings", d: DLQ{Plugin: builtinLogPlugin}, want: true},
		{name: "log with payload false", d: DLQ{Plugin: builtinLogPlugin, Settings: map[string]string{"payload": "false"}}, want: true},
		{name: "log with payload empty", d: DLQ{Plugin: builtinLogPlugin, Settings: map[string]string{"payload": ""}}, want: true},
		{name: "log with payload true", d: DLQ{Plugin: builtinLogPlugin, Settings: map[string]string{"payload": "true"}}, want: false},
		{name: "log with payload TRUE", d: DLQ{Plugin: builtinLogPlugin, Settings: map[string]string{"payload": "TRUE"}}, want: false},
		{name: "log with payload spaced", d: DLQ{Plugin: builtinLogPlugin, Settings: map[string]string{"payload": " true "}}, want: false},
		{name: "versioned log", d: DLQ{Plugin: "builtin:log@v0.8.0"}, want: true},
		{name: "log at latest", d: DLQ{Plugin: "builtin:log@latest"}, want: true},
		{name: "bare log", d: DLQ{Plugin: "log"}, want: true},
		{name: "bare versioned log", d: DLQ{Plugin: "log@v0.8.0"}, want: true},
		{name: "standalone log", d: DLQ{Plugin: "standalone:log"}, want: true},
		{name: "standalone log with payload", d: DLQ{Plugin: "standalone:log@v0.8.0", Settings: map[string]string{"payload": "true"}}, want: false},
		{name: "log-like name", d: DLQ{Plugin: "builtin:logger"}, want: false},
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

// TestDLQ_DropsRecordContent is the regression test for the v0.21.0 default-DLQ
// refusal: the default DLQ (builtin:log, window-size 1, threshold 0) logs only
// hashes but routes no nacked record (the first nack stops the pipeline and the
// record is not acked), so it must not be refused. A log DLQ without payload
// that does receive records (threshold above 0, or window-size 0) must be.
func TestDLQ_DropsRecordContent(t *testing.T) {
	t.Parallel()
	logDLQ := func(size, threshold int, settings map[string]string) DLQ {
		return DLQ{Plugin: builtinLogPlugin, Settings: settings, WindowSize: size, WindowNackThreshold: threshold}
	}
	payload := map[string]string{"payload": "true"}
	tests := []struct {
		name string
		d    DLQ
		want bool
	}{
		{name: "default DLQ", d: DefaultDLQ, want: false},
		{name: "log, threshold 0", d: logDLQ(10, 0, nil), want: false},
		{name: "log, threshold 1", d: logDLQ(10, 1, nil), want: true},
		{name: "log, window disabled", d: logDLQ(0, 0, nil), want: true},
		{name: "log with payload, threshold 1", d: logDLQ(10, 1, payload), want: false},
		{name: "log with payload, window disabled", d: logDLQ(0, 0, payload), want: false},
		{name: "non-log plugin, threshold 1", d: DLQ{Plugin: "builtin:file", WindowSize: 10, WindowNackThreshold: 1}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.DropsRecordContent(); got != tt.want {
				t.Fatalf("DropsRecordContent() = %v, want %v", got, tt.want)
			}
		})
	}
}
