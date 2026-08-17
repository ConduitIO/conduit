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

	"github.com/matryer/is"
)

func TestExtractCandidate_Shapes(t *testing.T) {
	is := is.New(t)

	for _, tc := range []struct {
		name     string
		raw      string
		contains string
	}{{
		name:     "bare yaml",
		raw:      validCandidate,
		contains: "builtin:generator",
	}, {
		name:     "fenced with a language tag",
		raw:      "```yaml\n" + validCandidate + "```",
		contains: "builtin:generator",
	}, {
		name:     "fenced without a language tag",
		raw:      "```\n" + validCandidate + "```",
		contains: "builtin:generator",
	}, {
		name:     "prose above the config is dropped",
		raw:      "Sure! Here is a pipeline that does what you asked:\n\n" + validCandidate,
		contains: "builtin:generator",
	}, {
		name:     "unterminated fence still yields the partial config",
		raw:      "```yaml\n" + validCandidate,
		contains: "builtin:generator",
	}, {
		name: "the block holding version: wins over an unrelated one",
		raw: "First, install it:\n\n```sh\nconduit connectors install postgres\n```\n\n" +
			"Then:\n\n```yaml\n" + validCandidate + "```",
		contains: "builtin:generator",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractCandidate(tc.raw)
			is.NoErr(err)
			is.True(strings.Contains(got, tc.contains))
			is.True(!strings.Contains(got, "```")) // fences never survive
		})
	}
}

// A prose-only reply and an empty reply are errors: there is nothing to
// validate, and inventing a candidate is the one thing this must never do.
func TestExtractCandidate_NothingUsable(t *testing.T) {
	is := is.New(t)

	for _, raw := range []string{
		"",
		"   \n\t ",
		"I'd be happy to help! Could you tell me which database you're using?",
	} {
		_, err := extractCandidate(raw)
		is.True(err != nil)
	}
}

// A "version:" key nested inside settings is not the document root, so it
// must not be mistaken for the start of the config.
func TestExtractCandidate_NestedVersionKeyIsNotTheRoot(t *testing.T) {
	is := is.New(t)

	_, err := extractCandidate("here is some config:\n  settings:\n    version: 3\n")

	is.True(err != nil)
}

// Extraction never repairs: whatever comes back is the model's own bytes,
// which then face the same parser and validator a hand-written file faces.
func TestExtractCandidate_DoesNotRepairTheYAML(t *testing.T) {
	is := is.New(t)
	broken := "version: \"2.2\"\npipelines:\n  - id: x\n     bad-indent: true\n"

	got, err := extractCandidate("```yaml\n" + broken + "```")

	is.NoErr(err)
	is.Equal(strings.TrimSpace(got), strings.TrimSpace(broken))
}
