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

package ui

import (
	"fmt"
	"io"
)

// findingIndent is the indentation (in spaces) for a finding's message and
// suggestion lines, per the CLI output conventions' located-finding layout.
const findingIndent = "    "

// RenderFinding writes one located finding to w in the canonical layout (CLI
// output conventions §2) — the single template every command that reports a
// finding renders identically, so `doctor` and `pipelines validate` never
// diverge on this the way their design docs originally did (one used a
// trailing "└" line, the other an inline pointer):
//
//	✗ config.field_required   /connectors/0/plugin
//	    connector "pg-source": "plugin" is mandatory
//	    → set connectors[0].plugin (e.g. "builtin:postgres")
//
// glyph is the caller-resolved glyph string (typically Renderer.Glyph's
// result) — this function does no glyph/color selection itself. code and
// configPath render on line 1, separated from the glyph and from each other
// by the layout's fixed spacing; configPath is omitted (along with its
// separating space) when empty, since not every finding has one (a check
// result's ConfigPath is optional). message and suggestion are each on
// their own 4-space-indented line, in order, and omitted entirely when
// empty — there is no trailing "└" line.
func RenderFinding(w io.Writer, glyph, code, configPath, message, suggestion string) {
	line := glyph
	if code != "" {
		line += " " + code
	}
	if configPath != "" {
		line += "   " + configPath
	}
	fmt.Fprintln(w, line)

	if message != "" {
		fmt.Fprintln(w, findingIndent+message)
	}
	if suggestion != "" {
		fmt.Fprintln(w, findingIndent+"→ "+suggestion)
	}
}
