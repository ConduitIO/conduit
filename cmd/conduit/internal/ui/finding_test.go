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
	"bytes"
	"testing"

	"github.com/matryer/is"
)

// TestRenderFinding_CanonicalLayout locks the exact byte layout from the CLI
// output conventions doc (§2): glyph + code + configPath on line 1, message
// indented 4 spaces, "→ suggestion" indented 4 spaces, no trailing "└" line.
// A byte-for-byte match here is what keeps doctor and pipelines validate
// from re-diverging the way their original design docs did.
func TestRenderFinding_CanonicalLayout(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	RenderFinding(&buf, "✗", "config.field_required", "/connectors/0/plugin",
		`connector "pg-source": "plugin" is mandatory`,
		`set connectors[0].plugin (e.g. "builtin:postgres")`)

	want := "✗ config.field_required   /connectors/0/plugin\n" +
		"    connector \"pg-source\": \"plugin\" is mandatory\n" +
		"    → set connectors[0].plugin (e.g. \"builtin:postgres\")\n"

	is.Equal(buf.String(), want)
}

func TestRenderFinding_NoConfigPath(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	RenderFinding(&buf, "✗", "common.unavailable", "", "server unreachable", "start the server first")

	want := "✗ common.unavailable\n" +
		"    server unreachable\n" +
		"    → start the server first\n"

	is.Equal(buf.String(), want)
}

func TestRenderFinding_NoSuggestion(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	RenderFinding(&buf, "⚠", "some.warning", "/x/y", "something worth noting", "")

	want := "⚠ some.warning   /x/y\n" +
		"    something worth noting\n"

	is.Equal(buf.String(), want)
}

func TestRenderFinding_NoMessage(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	RenderFinding(&buf, "✓", "check.name", "", "", "")

	want := "✓ check.name\n"

	is.Equal(buf.String(), want)
}

func TestRenderFinding_ASCIIGlyph(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	RenderFinding(&buf, "[FAIL]", "config.field_required", "/connectors/0/plugin", "msg", "fix it")

	want := "[FAIL] config.field_required   /connectors/0/plugin\n" +
		"    msg\n" +
		"    → fix it\n"

	is.Equal(buf.String(), want)
}
