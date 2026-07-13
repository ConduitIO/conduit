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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/llmsgen/allcodes"
	"github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/matryer/is"
)

// repoRoot locates the module root once per test binary run — every test in
// this file that reads the committed llms.txt/llms-full.txt or scans the
// repo needs it.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := findModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	return root
}

// TestGenerator_MatchesCommittedFiles is the drift-guard itself, runnable
// as a fast local unit test (AC-1's "round-trip against committed output"
// plan, and AC-6: a hand edit to the committed file that the generator
// would not reproduce is caught here, not just in CI). If this test is
// red, `make generate` (or `go run ./cmd/conduit/internal/llmsgen`) and
// commit the result.
func TestGenerator_MatchesCommittedFiles(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"llms.txt", renderLLMSTxt(data)},
		{"llms-full.txt", renderLLMSFullTxt(data)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want, err := os.ReadFile(filepath.Join(root, tc.name))
			is.NoErr(err)

			got := normalizeTrailingNewline(tc.content)
			if string(want) != got {
				t.Errorf(
					"%s is stale (committed file doesn't match a fresh generation): "+
						"run `make generate` and commit the result.\n--- want len=%d, got len=%d ---",
					tc.name, len(want), len(got),
				)
			}
		})
	}
}

// TestGenerator_Idempotent is AC-1: generating twice from the same source
// state produces byte-identical output, both within one process run
// (this test) and, per TestGenerator_MatchesCommittedFiles above, against
// a prior run's committed result.
func TestGenerator_Idempotent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data1, err := buildGeneratedData(root)
	is.NoErr(err)
	data2, err := buildGeneratedData(root)
	is.NoErr(err)

	is.Equal(renderLLMSTxt(data1), renderLLMSTxt(data2))
	is.Equal(renderLLMSFullTxt(data1), renderLLMSFullTxt(data2))
}

// nondeterministicTokenPatterns are the classic offenders AC-7 rules out:
// timestamps/dates, absolute filesystem paths, a hostname, a Go toolchain
// version string, and any debug.BuildInfo-shaped module-version line. None
// of these can appear in llms.txt/llms-full.txt without breaking
// determinism across environments (design doc D3, Adversarial review).
var nondeterministicTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}`), // ISO timestamp
	regexp.MustCompile(`/Users/[A-Za-z0-9_.\-]+`),            // macOS absolute home path
	regexp.MustCompile(`/home/[A-Za-z0-9_.\-]+`),             // Linux absolute home path
	regexp.MustCompile(`\bgo1\.\d+(\.\d+)?\b`),               // Go toolchain version (go1.25.0)
	regexp.MustCompile(`(?i)\bbuildinfo\b`),                  // debug.BuildInfo-derived string
}

// TestNoNondeterministicTokens is AC-7: neither committed file may contain
// a timestamp, absolute path, hostname, Go version, or BuildInfo-derived
// string.
func TestNoNondeterministicTokens(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	for _, name := range []string{"llms.txt", "llms-full.txt"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		is.NoErr(err)

		for _, pattern := range nondeterministicTokenPatterns {
			if m := pattern.FindString(string(content)); m != "" {
				t.Errorf("%s contains a nondeterministic token %q (matched %s)", name, m, pattern)
			}
		}
	}
}

// TestEncoding is AC-8: both files are UTF-8, no BOM, LF-only line endings,
// exactly one trailing newline.
func TestEncoding(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	for _, name := range []string{"llms.txt", "llms-full.txt"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		is.NoErr(err)

		is.True(len(content) > 0)
		is.True(content[0] != 0xEF) // no UTF-8 BOM (EF BB BF)
		is.True(!strings.Contains(string(content), "\r"))
		is.True(strings.HasSuffix(string(content), "\n"))
		is.True(!strings.HasSuffix(string(content), "\n\n")) // exactly one trailing newline
	}
}

// TestGeneratedHeader is AC-16: both files start with the DO NOT EDIT
// generated-file marker.
func TestGeneratedHeader(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	for _, name := range []string{"llms.txt", "llms-full.txt"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		is.NoErr(err)
		is.True(strings.HasPrefix(string(content), generatedHeader))
	}
}

// TestLLMSTxt_SpecShape is AC-15: llms.txt has exactly one H1, a single
// blockquote summary immediately after (allowing the DO NOT EDIT comment
// line before it), and ## sections thereafter — the llmstxt.org shape.
func TestLLMSTxt_SpecShape(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	content, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	is.NoErr(err)
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")

	is.True(len(lines) > 3)
	is.Equal(lines[0], generatedHeader)
	is.True(strings.HasPrefix(lines[1], "# ") && !strings.HasPrefix(lines[1], "## "))

	// Exactly one H1.
	h1Count := 0
	blockquoteRun := 0
	h2Count := 0
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## "):
			h1Count++
		case strings.HasPrefix(line, "## "):
			h2Count++
		case strings.HasPrefix(line, "> "):
			if i > 0 && strings.HasPrefix(lines[i-1], "> ") {
				blockquoteRun++
			}
		}
	}
	is.Equal(h1Count, 1)
	is.True(h2Count > 0)

	// Line 2 is blank, then the blockquote starts immediately at line 3.
	is.Equal(lines[2], "")
	is.True(strings.HasPrefix(lines[3], "> "))
}

// TestAllConnectorsPresent is AC-9: llms.txt lists exactly
// len(DefaultBuiltinConnectors) connectors, matching the live registry's
// key set by name.
func TestAllConnectorsPresent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)

	is.Equal(len(data.Connectors), len(builtin.DefaultBuiltinConnectors))

	gotNames := make([]string, len(data.Connectors))
	for i, c := range data.Connectors {
		gotNames[i] = c.Name
	}
	sort.Strings(gotNames)

	wantNames := make([]string, 0, len(builtin.DefaultBuiltinConnectors))
	for _, conn := range builtin.DefaultBuiltinConnectors {
		wantNames = append(wantNames, conn.NewSpecification().Name)
	}
	sort.Strings(wantNames)

	is.Equal(gotNames, wantNames)

	llmsTxt, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	is.NoErr(err)
	for _, name := range wantNames {
		is.True(strings.Contains(string(llmsTxt), "\n- "+name+" — "))
	}
}

// TestAllParamsPresent is AC-10: for each connector, every SourceParams and
// DestinationParams key appears in llms-full.txt.
func TestAllParamsPresent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	full, err := os.ReadFile(filepath.Join(root, "llms-full.txt"))
	is.NoErr(err)
	fullStr := string(full)

	for _, conn := range builtin.DefaultBuiltinConnectors {
		spec := conn.NewSpecification()
		for key := range spec.SourceParams {
			is.True(strings.Contains(fullStr, "`"+key+"`"))
		}
		for key := range spec.DestinationParams {
			is.True(strings.Contains(fullStr, "`"+key+"`"))
		}
	}
}

// TestAllErrorCodesPresent is AC-11: every reason in conduiterr.Codes()
// appears in llms-full.txt, and llms.txt's count line matches len(Codes()).
func TestAllErrorCodesPresent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	full, err := os.ReadFile(filepath.Join(root, "llms-full.txt"))
	is.NoErr(err)
	fullStr := string(full)

	codes := allcodes.Codes()
	is.True(len(codes) > 0)
	for _, c := range codes {
		is.True(strings.Contains(fullStr, "`"+c.Reason()+"`"))
	}

	summary, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	is.NoErr(err)
	is.True(strings.Contains(string(summary), strconv.Itoa(len(codes))+" stable error codes"))
}

// TestAllCodesComplete is the D5/AC-12 barrel guard: the set of reasons the
// allcodes barrel exposes (what the generator actually reads) must exactly
// equal the set found by independently scanning every non-test .go file in
// the repo for Register(...) call-sites. If a new codes.go (or
// equivalently-shaped file) is added to the engine and not wired into
// allcodes's blank-import list, this test fails — the generator's
// completeness is enforced by this comparison, not by review vigilance.
func TestAllCodesComplete(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	scanned, err := scanRegisteredCodes(root)
	is.NoErr(err)

	scannedReasons := make(map[string]bool, len(scanned))
	for _, c := range scanned {
		if scannedReasons[c.Reason] {
			t.Fatalf("duplicate reason found by source scan: %q (in %s) — conduiterr.Register "+
				"would have panicked on this at init; the scanner should never see a dup", c.Reason, c.File)
		}
		scannedReasons[c.Reason] = true
	}

	barrelReasons := make(map[string]bool)
	for _, c := range allcodes.Codes() {
		barrelReasons[c.Reason()] = true
	}

	var onlyInSource, onlyInBarrel []string
	for r := range scannedReasons {
		if !barrelReasons[r] {
			onlyInSource = append(onlyInSource, r)
		}
	}
	for r := range barrelReasons {
		if !scannedReasons[r] {
			onlyInBarrel = append(onlyInBarrel, r)
		}
	}
	sort.Strings(onlyInSource)
	sort.Strings(onlyInBarrel)

	if len(onlyInSource) > 0 {
		t.Errorf("reasons registered in source but not visible through allcodes barrel "+
			"(add the declaring package to allcodes.go's blank-import list): %v", onlyInSource)
	}
	if len(onlyInBarrel) > 0 {
		t.Errorf("reasons visible through allcodes but not found by the source scan "+
			"(scanRegisteredCodes bug, or a non-literal reason argument): %v", onlyInBarrel)
	}
}

// TestAllMCPToolsPresent is AC-13: every name in mcp.Catalog() appears in
// both files with the correct read/write classification, matching
// server_test.go's readToolNames/writeToolNames split.
func TestAllMCPToolsPresent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	catalog := mcp.Catalog()
	is.True(len(catalog) > 0)

	full, err := os.ReadFile(filepath.Join(root, "llms-full.txt"))
	is.NoErr(err)
	fullStr := string(full)

	summary, err := os.ReadFile(filepath.Join(root, "llms.txt"))
	is.NoErr(err)
	summaryStr := string(summary)

	for _, info := range catalog {
		is.True(strings.Contains(fullStr, "`"+info.Name+"`"))
		is.True(strings.Contains(summaryStr, info.Name))

		if info.Mutates {
			is.True(strings.Contains(fullStr, "write (requires --allow-mutations)"))
		}
	}

	is.True(strings.Contains(summaryStr, strconv.Itoa(len(catalog))+" tools"))
}

// TestConfigSchemaFieldsPresent is AC-14: every exported field of the five
// v2 structs appears in llms-full.txt with its exact YAML key (including
// the historically easy-to-typo ones — dead-letter-queue, window-size,
// window-nack-threshold), and both the latest (2.2) and legacy (1.1)
// version strings are present.
func TestConfigSchemaFieldsPresent(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	full, err := os.ReadFile(filepath.Join(root, "llms-full.txt"))
	is.NoErr(err)
	fullStr := string(full)

	for _, key := range []string{
		"dead-letter-queue", "window-size", "window-nack-threshold",
		"version", "pipelines", "connectors", "processors", "condition", "plugin",
	} {
		is.True(strings.Contains(fullStr, "`"+key+"`"))
	}

	cfg := gatherConfigSchema()
	is.Equal(cfg.LatestVersion, "2.2")
	is.Equal(cfg.LegacyVersion, "1.1")
	is.True(strings.Contains(fullStr, "2.2 (latest)"))
	is.True(strings.Contains(fullStr, "Legacy v1 (1.1)"))
}
