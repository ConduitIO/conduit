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

package generate

import (
	"strings"
	"testing"
)

// TestPromptDrift_ReplacedSystemPromptFails is the mutation test for the
// replay gate: a transcript recorded under the real prompt, replayed after
// BuildSystemPrompt was replaced with "You are a helpful assistant.", must
// classify as drifted and produce the re-capture failure. Before the gate
// existed this only produced a ::warning and the replay stayed green.
func TestPromptDrift_ReplacedSystemPromptFails(t *testing.T) {
	reqs, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tr := validTranscript(reqs[0]) // recorded under the real prompt

	mutated := sha256Hex("You are a helpful assistant.") // what the current prompt would hash to
	if got := classifyStaleness(tr, mutated, CatalogFingerprint()); got == StalenessFresh {
		t.Fatalf("a transcript captured under a different system prompt classified %s", got)
	}

	msg := promptDriftFailure([]string{"anthropic/claude-sonnet-5/" + reqs[0].ID})
	for _, want := range []string{"prompt changed; re-capture with", "generate-capture", reqs[0].ID, envReplayUpdate} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message missing %q:\n%s", want, msg)
		}
	}
}

// TestPromptDrift_FreshProducesNoFailure pins the other direction: matching
// hashes never fail, so the gate is not a permanent red.
func TestPromptDrift_FreshProducesNoFailure(t *testing.T) {
	reqs, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tr := validTranscript(reqs[0])
	current := sha256Hex(BuildSystemPrompt(BuiltinCatalog()))
	if got := classifyStaleness(tr, current, CatalogFingerprint()); got != StalenessFresh {
		t.Fatalf("matching hashes classified %s", got)
	}
	if msg := promptDriftFailure(nil); msg != "" {
		t.Fatalf("no drift must not fail, got: %s", msg)
	}
}

// TestReplayOutcome pins both modes. Strict fails on drift; the default only
// warns, and a golden mismatch under drift is informational. Without drift a
// golden mismatch fails in both.
func TestReplayOutcome(t *testing.T) {
	drift := []string{"anthropic/claude-sonnet-5/req-a"}
	const diff = "replay result differs"
	for _, tc := range []struct {
		name       string
		strict     bool
		drifted    []string
		goldenDiff string
		wantFail   string // substring, "" = must pass
		wantWarn   string
	}{
		{"strict drift fails", true, drift, "", "prompt changed; re-capture with", ""},
		{"strict drift with golden diff fails", true, drift, diff, "prompt changed; re-capture with", ""},
		{"default drift warns", false, drift, "", "", "prompt changed since capture; replay results are not representative; re-capture with"},
		{"default drift, golden diff is informational", false, drift, diff, "", diff},
		{"default no drift, golden diff fails", false, nil, diff, diff, ""},
		{"strict no drift, golden diff fails", true, nil, diff, diff, ""},
		{"clean passes", false, nil, "", "", ""},
		{"clean strict passes", true, nil, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail, warn := replayOutcome(tc.strict, tc.drifted, tc.goldenDiff)
			if (tc.wantFail == "") != (fail == "") || !strings.Contains(fail, tc.wantFail) {
				t.Errorf("fail = %q, want containing %q", fail, tc.wantFail)
			}
			if (tc.wantWarn == "") != (warn == "") || !strings.Contains(warn, tc.wantWarn) {
				t.Errorf("warn = %q, want containing %q", warn, tc.wantWarn)
			}
		})
	}
}
