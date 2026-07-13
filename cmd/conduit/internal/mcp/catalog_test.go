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

package mcp

import (
	"slices"
	"sort"
	"testing"

	"github.com/matryer/is"
)

// TestCatalog_MatchesRegisteredToolSets is the D4 contract test: Catalog()'s
// read set and write set (by Mutates) must exactly equal readToolNames and
// writeToolNames — the same sets server_test.go's
// TestNewServer_WriteToolsGatedByAllowMutations exercises through the live
// server. If a tool is added to NewServer without a matching catalog.go
// entry, tool() panics at server construction (caught by every other test in
// this package); if a catalog.go entry's Mutates disagrees with where
// NewServer actually registers it, this test catches that drift directly.
func TestCatalog_MatchesRegisteredToolSets(t *testing.T) {
	is := is.New(t)

	all := Catalog()

	// Sorted by Name (Catalog's documented contract).
	is.True(sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Name < all[j].Name }))

	var gotRead, gotWrite []string
	for _, info := range all {
		if info.Mutates {
			gotWrite = append(gotWrite, info.Name)
		} else {
			gotRead = append(gotRead, info.Name)
		}
	}
	sort.Strings(gotRead)
	sort.Strings(gotWrite)

	wantRead := append([]string(nil), readToolNames...)
	sort.Strings(wantRead)
	wantWrite := append([]string(nil), writeToolNames...)
	sort.Strings(wantWrite)

	is.Equal(gotRead, wantRead)
	is.Equal(gotWrite, wantWrite)

	// No duplicate names, no empty descriptions — the generator (and an
	// agent reading llms.txt) depends on both.
	seen := make(map[string]bool, len(all))
	for _, info := range all {
		is.True(!seen[info.Name]) // no duplicate tool name
		seen[info.Name] = true
		is.True(info.Description != "") // every tool has a description
	}
}

// TestTool_PanicsOnUnknownName documents tool()'s fail-loud behavior: a
// Tool* const used in NewServer without a Catalog() entry is a programming
// error caught at server construction, not a silently-empty description.
func TestTool_PanicsOnUnknownName(t *testing.T) {
	is := is.New(t)

	defer func() {
		r := recover()
		is.True(r != nil)
	}()
	_ = tool("not_a_registered_tool")
}

// TestCatalog_ReturnsACopy asserts mutating the slice Catalog() returns
// cannot corrupt the package-level catalog for the next caller.
func TestCatalog_ReturnsACopy(t *testing.T) {
	is := is.New(t)

	first := Catalog()
	if len(first) > 0 {
		first[0].Name = "mutated"
	}
	second := Catalog()
	is.True(!slices.ContainsFunc(second, func(info ToolInfo) bool { return info.Name == "mutated" }))
}
