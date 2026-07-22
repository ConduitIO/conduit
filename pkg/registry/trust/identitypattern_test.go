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

package trust_test

import (
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/trust"
)

func TestValidateIdentityPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{
			name:    "valid real-world pattern",
			pattern: `^https://github\.com/ConduitIO/conduit-connector-postgres/\.github/workflows/publish\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`,
			wantErr: false,
		},
		{
			name:    "not anchored at start",
			pattern: `https://github\.com/a/b$`,
			wantErr: true,
		},
		{
			name:    "not anchored at end",
			pattern: `^https://github\.com/a/b`,
			wantErr: true,
		},
		{
			name:    "empty",
			pattern: ``,
			wantErr: true,
		},
		{
			name:    "just anchors, no content",
			pattern: `^$`,
			wantErr: true,
		},
		{
			name:    "overly broad wildcard, still anchored",
			pattern: `^.*$`,
			wantErr: true,
		},
		{
			name:    "anchored but missing github.com",
			pattern: `^https://gitlab\.com/a/b/\.gitlab-ci\.yml$`,
			wantErr: true,
		},
		{
			name:    "github.com present but owner/repo not pinned (wildcard after)",
			pattern: `^https://github\.com/.*$`,
			wantErr: true,
		},
		{
			name:    "only owner pinned, repo left open",
			pattern: `^https://github\.com/ConduitIO/.*$`,
			wantErr: true,
		},
		{
			name:    "inline multiline flag",
			pattern: `^(?m)https://github\.com/a/b/c$`,
			wantErr: true,
		},
		{
			name:    "inline dotall flag",
			pattern: `^(?s)https://github\.com/a/b/c$`,
			wantErr: true,
		},
		{
			// An UNESCAPED dot in "github.com" is a real regex metacharacter
			// (matches any single character), so this pattern would also
			// match "githubXcom/..." — exactly the confusability the
			// tightness check exists to catch. It must fail, not pass: this
			// is a deliberate negative case, not a false positive.
			name:    "unescaped dot is a metacharacter, not a tight literal match",
			pattern: `^https://github.com/ConduitIO/conduit-connector-postgres/workflow$`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			err := trust.ValidateIdentityPattern(tt.pattern)
			if tt.wantErr {
				is.True(err != nil)
			} else {
				is.NoErr(err)
			}
		})
	}
}

// FuzzIdentityPattern is not one of P0-2's two named targets, but plan-v2
// §15.1 separately lists "identity-pattern anchoring/prefix/inline-flag
// rejection (fuzz target)" as a required trust-package test, and
// ValidateIdentityPattern parses caller-controlled strings with hand-rolled
// scanning logic (literalPrefix) — exactly the kind of code where a fuzz
// target is cheap insurance against a panic on a byte sequence no unit test
// happened to think of.
func FuzzIdentityPattern(f *testing.F) {
	f.Add(`^https://github\.com/ConduitIO/conduit-connector-postgres/\.github/workflows/publish\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`)
	f.Add(`^.*$`)
	f.Add(`(?m)^github.com/a/b$`)
	f.Add(``)
	f.Add(`^$`)
	f.Add(`^\$`)
	f.Add(`^` + string([]byte{0xff, 0xfe}) + `$`)

	f.Fuzz(func(t *testing.T, pattern string) {
		// The only invariant under fuzzing: never panic.
		_ = trust.ValidateIdentityPattern(pattern)
	})
}
