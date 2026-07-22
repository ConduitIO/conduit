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

package trust

import (
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// disallowedInlineFlags are inline RE2 flags that can weaken ^/$ anchoring
// under Go's regexp engine (multi-line mode makes ^/$ match at every
// newline; dot-matches-newline mode is orthogonal but listed alongside it
// in every combination index-CI must reject) — R-1's "Registration
// checklist adds pattern-tightness" addition, and plan-v2 §10's reviewer
// checklist item.
var disallowedInlineFlags = []string{"(?m)", "(?s)", "(?ms)", "(?sm)"}

// ValidateIdentityPattern checks a publisher.expectedIdentityPattern value
// against the registration-checklist tightness rules (R-1's Resolved
// decisions, "Registration checklist adds pattern-tightness"; plan-v2
// §10): fully anchored, no inline regex flags that could weaken that
// anchoring, and a literal (non-metachar) prefix that actually contains
// github.com/<owner>/<repo>/ rather than a broad wildcard like ^.*$ — schema
// -level anchoring alone (JSON Schema's `pattern: ^\^.*\$$`) doesn't stop
// an overly-broad-but-still-anchored expression.
//
// This is index-CI lint-time validation (and available for defense-in-depth
// reuse by a client before ever calling cosign) — it is NOT itself a client
// verification decision, so it returns a plain error, not a
// conduiterr.Code: nothing in the canonical error table (plan-v2 §4) names
// a dedicated code for "identity pattern fails the tightness lint", and
// inventing one here would be guessing ahead of index-CI's actual PR-2/PR-3
// wiring (flagged explicitly as a plan-v2 ambiguity in this PR's
// description).
func ValidateIdentityPattern(pattern string) error {
	if len(pattern) < 2 || pattern[0] != '^' || pattern[len(pattern)-1] != '$' {
		return cerrors.Errorf("expectedIdentityPattern must be fully anchored (^...$): %q", pattern)
	}

	for _, flag := range disallowedInlineFlags {
		if strings.Contains(pattern, flag) {
			return cerrors.Errorf(
				"expectedIdentityPattern must not use inline regex flag %q, which can weaken ^/$ anchoring under RE2: %q",
				flag, pattern,
			)
		}
	}

	const wantPrefix = "github.com/"
	prefix := literalPrefix(pattern[1 : len(pattern)-1]) // strip ^ and $
	idx := strings.Index(prefix, wantPrefix)
	if idx == -1 {
		return cerrors.Errorf(
			"expectedIdentityPattern's literal (non-metachar) prefix must contain %q so the owner/repo is actually pinned, not just anchored: %q",
			wantPrefix, pattern,
		)
	}

	rest := prefix[idx+len(wantPrefix):]
	if strings.Count(rest, "/") < 2 {
		return cerrors.Errorf(
			"expectedIdentityPattern's literal prefix must pin both an owner and a repo after %q (need at least two more literal path segments): %q",
			wantPrefix, pattern,
		)
	}
	return nil
}

// literalPrefix returns the longest leading run of s that is unambiguously
// literal: a backslash-escaped character (\X) contributes its literal X —
// treating \. as the literal dot every existing identity pattern uses
// (e.g. "github\.com/"), not as a metacharacter — and the scan stops at
// the first unescaped regex metacharacter.
func literalPrefix(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if strings.ContainsRune(`.*+?()[]{}|^$`, rune(c)) {
			break
		}
		b.WriteByte(c)
	}
	return b.String()
}
