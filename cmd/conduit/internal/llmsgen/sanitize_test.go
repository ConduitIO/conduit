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

package main

import (
	"strings"
	"testing"

	"github.com/matryer/is"
)

func TestSanitizeConnectorDescription(t *testing.T) {
	is := is.New(t)

	in := strings.Join([]string{
		"Intro line.",
		"## How it works",
		"Body.",
		"### Details",
		"```yaml",
		"# this is a comment inside a fence, not a heading",
		"key: value",
		"```",
		"#### Closing ####",
	}, "\n")

	got := sanitizeConnectorDescription(in)

	// Headings outside fences become bold; the trailing ATX # is stripped.
	is.True(strings.Contains(got, "**How it works**"))
	is.True(strings.Contains(got, "**Details**"))
	is.True(strings.Contains(got, "**Closing**"))
	// No ATX heading lines survive outside a fence.
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			break // stop before the fenced block for this assertion
		}
		is.True(!isATXHeading(trimmed))
	}
	// The comment inside the fence is untouched.
	is.True(strings.Contains(got, "# this is a comment inside a fence, not a heading"))
	// Non-heading text is preserved verbatim.
	is.True(strings.Contains(got, "Intro line."))
	is.True(strings.Contains(got, "Body."))
}

// TestRenderConnectorsSection_NoStrayHeadings is the structural guard for the M1
// fix: a connector Description containing README-style headings must not bleed
// out as sections in llms-full.txt. Only the section's own "## 2." header and
// each connector's "### name" header may appear.
func TestRenderConnectorsSection_NoStrayHeadings(t *testing.T) {
	is := is.New(t)

	conns := []connectorInfo{{
		Name:        "demo",
		Version:     "v1.2.3",
		Summary:     "A demo connector.",
		Description: "Intro.\n\n## Injected Section\n\nText.\n\n### Injected Sub\n\nMore.",
		Author:      "Meroxa",
	}}

	var b strings.Builder
	renderConnectorsSection(&b, conns)
	out := b.String()

	// The injected headings are neutralized to bold, not emitted as headings.
	is.True(strings.Contains(out, "**Injected Section**"))
	is.True(strings.Contains(out, "**Injected Sub**"))
	is.True(!strings.Contains(out, "## Injected"))
	is.True(!strings.Contains(out, "### Injected"))

	// Exactly one H2 (the section header) and the allowed H3s (connector name +
	// the two param subsections) — nothing bleeding out of the description.
	var h2, strayH3 int
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			h2++
		case strings.HasPrefix(line, "### "):
			// "### demo (v1.2.3)" is the only allowed H3 here.
			if !strings.HasPrefix(line, "### demo (") {
				strayH3++
			}
		}
	}
	is.Equal(h2, 1)      // only "## 2. Built-in connectors (1)"
	is.Equal(strayH3, 0) // no description headings leaked to H3
}

// isATXHeading reports whether line begins an ATX heading (mirrors the logic in
// atxHeadingToBold for the test's own assertion).
func isATXHeading(line string) bool {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && n < len(line) && line[n] == ' '
}
