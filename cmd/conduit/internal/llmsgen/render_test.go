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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/matryer/is"
)

// These are formatter-fixture tests (design doc's golden-file test plan,
// item 3): each renderer is fed small, synthetic, hand-constructed input —
// never the real registries — and its output is compared byte-for-byte to
// a checked-in golden file under testdata/. A formatting regression (a
// column reordered, an escape rule broken) shows up here as a small,
// readable diff, instead of only as a multi-thousand-line diff against the
// real committed llms-full.txt.
//
// Per this repo's convention (pkg/scaffold/template/rewrite_test.go), there
// is no -update flag: regenerate a golden fixture by hand (print the
// renderer's output, review the diff, then overwrite the file) and commit
// the reviewed result — a flag that silently overwrites golden output on
// every red run would defeat the point of the test.

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden fixture %s: %v", name, err)
	}
	return string(b)
}

func TestRenderConnectorsSection_Golden(t *testing.T) {
	is := is.New(t)

	connectors := []connectorInfo{
		{
			Name:        "widget",
			Summary:     "A synthetic connector used only by this test.",
			Description: "Longer description.\nSecond line.",
			Version:     "v1.2.3",
			Author:      "Test Author",
			SourceParams: []paramInfo{
				{Key: "path", Type: "string", Default: "", Required: true, Description: "File path.", Validations: []string{"required"}},
				{Key: "batch.size", Type: "int", Default: "100", Required: false, Description: "Batch size.", Validations: []string{"greater-than(0)", "less-than(10000)"}},
			},
			DestinationParams: nil,
		},
	}

	var b strings.Builder
	renderConnectorsSection(&b, connectors)

	is.Equal(b.String(), readGolden(t, "connectors_section.golden"))
}

func TestRenderParamsTable_Empty_Golden(t *testing.T) {
	is := is.New(t)

	var b strings.Builder
	renderParamsTable(&b, nil)

	is.Equal(b.String(), readGolden(t, "params_table_empty.golden"))
}

func TestRenderErrorCodesSection_Golden(t *testing.T) {
	is := is.New(t)

	codes := []errorCodeInfo{
		{Reason: "widget.not_found", GRPCCode: "NotFound", Description: "Widget not found."},
		{Reason: "widget.pipe|broken", GRPCCode: "Internal", Description: "Contains a\nnewline and a | pipe."},
	}

	var b strings.Builder
	renderErrorCodesSection(&b, codes)

	is.Equal(b.String(), readGolden(t, "error_codes_section.golden"))
}

func TestRenderMCPToolsSection_Golden(t *testing.T) {
	is := is.New(t)

	tools := []mcp.ToolInfo{
		{Name: "write_tool", Description: "Mutates something.", Mutates: true},
		{Name: "read_tool", Description: "Reads something.", Mutates: false},
	}

	var b strings.Builder
	renderMCPToolsSection(&b, tools)

	is.Equal(b.String(), readGolden(t, "mcp_tools_section.golden"))
}

func TestRenderConfigSchemaSection_Golden(t *testing.T) {
	is := is.New(t)

	cfg := configSchema{
		LatestVersion: "9.9",
		Versions:      []string{"9.0", "9.9"},
		Structs: []configStruct{
			{Name: "Widget", Fields: []configField{
				{GoField: "ID", YAMLKey: "id", GoType: "string", Required: true},
				{GoField: "Count", YAMLKey: "count", GoType: "*int", Required: false},
			}},
		},
		LegacyVersion: "8.1",
	}

	var b strings.Builder
	renderConfigSchemaSection(&b, cfg)

	is.Equal(b.String(), readGolden(t, "config_schema_section.golden"))
}

func TestWriteBlockquote_Wraps(t *testing.T) {
	is := is.New(t)

	var b strings.Builder
	writeBlockquote(&b, "one two three four five six seven eight nine ten")
	got := b.String()

	is.True(strings.HasPrefix(got, "> "))
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		is.True(strings.HasPrefix(line, "> "))
	}
}

func TestEscapeCell(t *testing.T) {
	is := is.New(t)

	is.Equal(escapeCell("a|b"), `a\|b`)
	is.Equal(escapeCell("line1\nline2"), "line1 line2")
	is.Equal(escapeCell("line1\r\nline2"), "line1 line2")
	is.Equal(escapeCell("  spaced  "), "spaced")
}

func TestFriendlyType(t *testing.T) {
	is := is.New(t)

	is.Equal(friendlyType(reflect.TypeOf("")), "string")
	is.Equal(friendlyType(reflect.TypeOf(0)), "int")
}
