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
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/log"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// MaxTurnCompletionBytes is the hard per-turn cap on a Turn.CompletionText's
// length (plan §5). A generated pipeline config is under 1.5 KiB in this
// package's own corpus, so anything past this cap is truncation or narration
// bloat — and either way is a bigger public-repo surface than a
// maintenance-only eval fixture needs. The future capture tool (A5a-3) must
// refuse to write a turn over this cap; ScanTranscriptForSecrets enforces it
// again here so a hand-edit (or a future capture-path regression) can't slip
// an oversized blob past review.
const MaxTurnCompletionBytes = 8 * 1024

// credentialShapedKey matches a connector settings key whose NAME suggests it
// might hold a real credential, independent of whether its value happens to
// match a known secret format. This is the structural half of §5's
// redaction: secretPatterns below catches a value that LOOKS like a known
// credential shape; this catches one that doesn't — e.g. a DSN with a
// plaintext password embedded ("postgres://user:hunter2@host/db") — by
// checking the key a model would plausibly have used to name it, per §5's
// "a config is under 1.5 KiB, so a plausible DSN is exactly the kind of
// thing that needs a structural check, not just a format-shaped one."
var credentialShapedKey = regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key|credential|dsn|conn)`)

// secretPatterns is §5's deny-list: value SHAPES a real credential could
// take. A finding never echoes the matched substring — the fact that
// something matched secretPatterns[i] is what a reviewer needs, and printing
// the match into a CI log would turn the scan's own output into the leak it
// exists to catch.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Anthropic API key (sk-ant-…)", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{10,}`)},
	{"generic secret-key-shaped token (sk-…)", regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)},
	{"GitHub token (ghp_/github_pat_/gho_…)", regexp.MustCompile(`\b(?:ghp_|github_pat_|gho_)[A-Za-z0-9_]{20,}\b`)},
	{"AWS access key id (AKIA…)", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"PEM private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"JWT", regexp.MustCompile(`\bey[A-Za-z0-9_-]{10,}\.ey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"long base64 run (>=80 chars)", regexp.MustCompile(`\b[A-Za-z0-9+/]{80,}={0,2}\b`)},
}

// scanTextForSecrets returns the name of every secretPatterns entry that
// matches text, in table order, or nil when text is clean.
func scanTextForSecrets(text string) []string {
	var found []string
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			found = append(found, p.name)
		}
	}
	return found
}

// isPlaceholderValue reports whether v is an obvious placeholder rather than
// real credential material. BuildSystemPrompt instructs the model to emit
// the literal "TODO" for any secret (grounding.go's rule "emit the literal
// placeholder TODO"), so that is the expected shape; a handful of other
// common placeholder spellings are accepted too, since a model narrating
// around the rule ("TODO_REPLACE_WITH_YOUR_KEY", "<your-api-key>") is still
// refusing to fabricate a credential — which is what this check exists to
// confirm, not to enforce one exact literal string.
func isPlaceholderValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	upper := strings.ToUpper(v)
	for _, marker := range []string{"TODO", "CHANGEME", "REDACTED", "PLACEHOLDER", "EXAMPLE", "XXXX"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")
}

// scanCandidateSettingsForCredentials best-effort extracts and parses the
// pipeline YAML out of rawCompletion (the same extractCandidate +
// yamlparser pipeline generate.go and semantic_score.go use) and flags every
// settings key — connector OR processor, pipeline-level or attached to a
// connector (allProcessors, semantic_score.go, flattens both) — that looks
// credential-shaped (credentialShapedKey) but whose value is not an obvious
// placeholder (isPlaceholderValue). Plan §5 says "any settings key", not
// "any connector settings key": a processor plugin (e.g. one calling an
// external enrichment API) has its own Settings map and can carry a
// credential-shaped key exactly the same way a connector can.
//
// A turn that fails to extract or parse yields no findings from THIS check —
// the deny-list scan (scanTextForSecrets) already covers the raw text
// regardless of whether it parses as a pipeline, and guessing at settings
// keys inside unparseable text would be inventing findings rather than
// reporting real ones.
func scanCandidateSettingsForCredentials(ctx context.Context, rawCompletion string) []string {
	candidate, err := extractCandidate(rawCompletion)
	if err != nil {
		return nil
	}
	pipelines, err := yamlparser.NewParser(log.Nop()).Parse(ctx, strings.NewReader(candidate))
	if err != nil || len(pipelines) == 0 {
		return nil
	}

	var findings []string
	for _, p := range pipelines {
		for _, c := range p.Connectors {
			findings = append(findings, credentialFindings("connector "+strconv.Quote(c.ID), c.Settings)...)
		}
		for _, proc := range allProcessors(p) {
			findings = append(findings, credentialFindings("processor "+strconv.Quote(proc.ID), proc.Settings)...)
		}
	}
	sort.Strings(findings) // map iteration order is random; findings must be deterministic
	return findings
}

// credentialFindings flags every entry in settings whose key looks
// credential-shaped but whose value is not an obvious placeholder, labeling
// each finding with subject (e.g. `connector "src"`).
func credentialFindings(subject string, settings map[string]string) []string {
	var findings []string
	for key, val := range settings {
		if credentialShapedKey.MatchString(key) && !isPlaceholderValue(val) {
			findings = append(findings, fmt.Sprintf(
				"%s settings key %q does not hold an obvious placeholder", subject, key,
			))
		}
	}
	return findings
}

// ScanTranscriptForSecrets is plan §5's redaction scanner, run over one
// committed Transcript. It never mutates, truncates, or redacts anything
// in place — a violation is only ever reported, so the fix is always a
// re-capture or a reviewed hand-edit, never a scanner side effect that could
// itself hide what was found.
//
// Three independent checks, findings from all three combined into one slice
// (nil when clean):
//
//  1. Every Turn.CompletionText is within MaxTurnCompletionBytes.
//  2. No Turn.CompletionText matches a secretPatterns entry.
//  3. No connector settings key that looks credential-shaped holds a
//     non-placeholder value, in the pipeline extracted from each turn's
//     completion text (scanCandidateSettingsForCredentials; best-effort,
//     independent of check 2 — an unparseable turn still gets checks 1 and 2).
//
// This is the ONLY secret-scanning mechanism in the repo today (the repo has
// no general secret-scanning workflow) — never call it defence-in-depth.
func ScanTranscriptForSecrets(ctx context.Context, t Transcript) []string {
	var findings []string
	for _, turn := range t.Turns {
		if n := len(turn.CompletionText); n > MaxTurnCompletionBytes {
			findings = append(findings, fmt.Sprintf(
				"transcript %q turn %d: completion text is %d bytes, exceeds the %d byte cap",
				t.RequestID, turn.N, n, MaxTurnCompletionBytes,
			))
		}
		for _, pattern := range scanTextForSecrets(turn.CompletionText) {
			findings = append(findings, fmt.Sprintf(
				"transcript %q turn %d: completion text matches deny-list pattern %q",
				t.RequestID, turn.N, pattern,
			))
		}
		for _, f := range scanCandidateSettingsForCredentials(ctx, turn.CompletionText) {
			findings = append(findings, fmt.Sprintf("transcript %q turn %d: %s", t.RequestID, turn.N, f))
		}
	}
	return findings
}
