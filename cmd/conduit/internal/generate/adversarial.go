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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// AdversarialFixture is one committed prompt-injection test case: a hostile
// natural-language request, paired with the specific guarantee the
// generation boundary must uphold against it.
//
// This is the fixture corpus behind v0.20 WS1 slice A4 (AC 1.10): "injection
// fixtures — prompts attempting env/secret exfiltration, fabricated plugin
// injection, or coerced auto-apply — produce no secret material and no
// apply. One test per attack class." See
// docs/design-documents/20260722-conduit-generate.md §4 (the never-auto-apply
// boundary) and §8 (error taxonomy) for the guarantees these fixtures probe.
//
// A fixture never asserts anything by itself — it is data a _test.go table
// test drives through the real Generate/ExecuteWithResult code path with a
// fake, in-memory provider (never a network call, never a real API key).
// What each id's test checks is documented at the test, not reconstructed
// from this struct's fields alone, because the check is a Go assertion about
// observable state (no file written outside the working directory, no
// secret string in a request, no plugin reference that survives
// ResolvePlugins) — not something this struct's shape could express on its
// own.
type AdversarialFixture struct {
	// ID is a stable, unique fixture identifier (kebab-case). Renaming one
	// breaks any report keyed on it, same discipline as Request.ID.
	ID string `yaml:"id"`
	// AttackClass groups fixtures by the acceptance-criterion category they
	// exercise: secret-exfiltration, fabricated-plugin, coerced-auto-apply,
	// output-path-escape, or feedback-instruction-override.
	AttackClass string `yaml:"attackClass"`
	// Prompt is the hostile natural-language request. It is what a test
	// hands to Generate/ExecuteWithResult as Input.Prompt — sent verbatim,
	// the same guarantee any other request gets (design §3), because a
	// prompt that got rewritten or sanitized before use would no longer be
	// testing what an attacker could actually submit.
	Prompt string `yaml:"prompt"`
	// MaliciousReply is the raw completion text a compromised, jailbroken,
	// or successfully-injected provider might return for this Prompt.
	// Empty when the attack's outcome does not depend on model output at
	// all — e.g. secret exfiltration, where the guarantee is that no real
	// secret ever reaches the completion REQUEST in the first place, so
	// nothing the model could reply with matters.
	MaliciousReply string `yaml:"maliciousReply,omitempty"`
	// MustNotHappen documents, for a human reviewing the corpus, the
	// specific bad outcome the fixture's test asserts against. Never parsed
	// or consumed by a test — it is the fixture's own paraphrase of the
	// assertion, kept next to the data it describes.
	MustNotHappen string `yaml:"mustNotHappen"`
	// Notes is free-text context: why this fixture exists, what code path
	// it exercises. Never consumed by a test.
	Notes string `yaml:"notes,omitempty"`
}

// LoadAdversarialFixtures reads and validates a committed adversarial-prompt
// corpus file (the shape testdata/adversarial_requests.yaml uses): every
// fixture must carry a non-empty, unique ID, a non-empty AttackClass, and a
// non-empty Prompt. Mirrors LoadRequests' validation discipline for the same
// reason: a corpus entry with a typo'd or duplicate ID silently shrinks (and
// reshapes) the set of attacks actually being tested, which is worse than a
// hard failure at load time.
func LoadAdversarialFixtures(path string) ([]AdversarialFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cerrors.Errorf("reading adversarial fixtures %q: %w", path, err)
	}

	var fixtures []AdversarialFixture
	if err := yaml.Unmarshal(data, &fixtures); err != nil {
		return nil, cerrors.Errorf("parsing adversarial fixtures %q: %w", path, err)
	}

	seen := make(map[string]bool, len(fixtures))
	for i, f := range fixtures {
		if f.ID == "" {
			return nil, cerrors.Errorf("adversarial fixtures %q: entry %d has no id", path, i)
		}
		if seen[f.ID] {
			return nil, cerrors.Errorf("adversarial fixtures %q: duplicate id %q", path, f.ID)
		}
		seen[f.ID] = true
		if f.AttackClass == "" {
			return nil, cerrors.Errorf("adversarial fixtures %q: entry %q has no attackClass", path, f.ID)
		}
		if f.Prompt == "" {
			return nil, cerrors.Errorf("adversarial fixtures %q: entry %q has no prompt", path, f.ID)
		}
	}

	return fixtures, nil
}

// FixturesByClass groups a loaded corpus by AttackClass, preserving each
// group's original order. Tests use this to assert coverage ("at least one
// fixture per attack class exists") without hardcoding the class list twice
// — the class list IS the map's key set.
func FixturesByClass(fixtures []AdversarialFixture) map[string][]AdversarialFixture {
	out := make(map[string][]AdversarialFixture)
	for _, f := range fixtures {
		out[f.AttackClass] = append(out[f.AttackClass], f)
	}
	return out
}
