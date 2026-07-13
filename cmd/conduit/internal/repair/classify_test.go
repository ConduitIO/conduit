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

package repair

import (
	"testing"

	"github.com/matryer/is"
)

// TestClassify_TableTest is AC-5: every ConfigPath a v1 producer can emit
// classifies as expected, and — the default-deny guarantee — anything this
// package does not explicitly recognize classifies FixClassDataPath, never
// FixClassSafe.
func TestClassify_TableTest(t *testing.T) {
	tests := []struct {
		name string
		path string
		want FixClass
	}{
		{"pipeline status", "/status", FixClassSafe},
		{"pipeline description", "/description", FixClassSafe},
		{"processor type (pipeline-level, document-rooted)", "/pipelines/0/processors/0/type", FixClassSafe},
		{"processor type (connector-nested, document-rooted)", "/pipelines/0/connectors/1/processors/0/type", FixClassSafe},
		{"processor workers", "/processors/0/workers", FixClassRestart},
		{"nested processor workers", "/connectors/0/processors/2/workers", FixClassRestart},

		{"connector settings", "/connectors/0/settings/table", FixClassDataPath},
		{"nested processor settings", "/connectors/0/processors/0/settings/key", FixClassDataPath},
		{"connector plugin", "/connectors/0/plugin", FixClassDataPath},
		{"connector type", "/connectors/0/type", FixClassDataPath},
		{"dlq field", "/dlq/plugin", FixClassDataPath},
		{"dead-letter-queue field (document-rooted)", "/pipelines/0/dead-letter-queue/plugin", FixClassDataPath},
		{"pipeline id", "/id", FixClassDataPath},
		{"connector id", "/connectors/0/id", FixClassDataPath},
		{"processor id", "/processors/0/id", FixClassDataPath},

		// Default-deny: never seen before, must NOT be safe.
		{"unrecognized field", "/some/made/up/field", FixClassDataPath},
		{"empty path", "", FixClassDataPath},
		{"root only", "/", FixClassDataPath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(classify(tc.path), tc.want)
		})
	}
}

// TestGateFix proves the Tier-1 escalation gate directly (design doc §4.2):
// a data_path fix is refused unless escalate is true; every other class is
// never gated here (Apply's default selection already excludes them, but
// gateFix itself must not second-guess an explicit safe/restart selection).
func TestGateFix(t *testing.T) {
	tests := []struct {
		name     string
		class    FixClass
		escalate bool
		wantSkip bool
		wantCode string
	}{
		{"data_path without escalate is refused", FixClassDataPath, false, true, CodeDataPathFixRefused.Reason()},
		{"data_path with escalate is allowed", FixClassDataPath, true, false, ""},
		{"safe is never gated", FixClassSafe, false, false, ""},
		{"restart is never gated", FixClassRestart, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			skip, reason := gateFix(ProposedFix{Class: tc.class}, tc.escalate)
			is.Equal(skip, tc.wantSkip)
			is.Equal(reason, tc.wantCode)
		})
	}
}
