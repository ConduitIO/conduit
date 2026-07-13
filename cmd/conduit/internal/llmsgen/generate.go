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

//go:generate go run github.com/conduitio/conduit/cmd/conduit/internal/llmsgen

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "llmsgen:", err)
		os.Exit(1)
	}
}

// run gathers all four sources and writes llms.txt/llms-full.txt to the
// repo root (the directory containing go.mod). Every step here is pure and
// individually unit-tested (llmsgen_test.go); run is the thin composition
// root — see buildGeneratedData for the part that's reused by tests.
func run() error {
	root, err := findModuleRoot(".")
	if err != nil {
		return err
	}

	data, err := buildGeneratedData(root)
	if err != nil {
		return err
	}

	if err := writeGeneratedFile(filepath.Join(root, "llms.txt"), renderLLMSTxt(data)); err != nil {
		return err
	}
	if err := writeGeneratedFile(filepath.Join(root, "llms-full.txt"), renderLLMSFullTxt(data)); err != nil {
		return err
	}
	return nil
}

// buildGeneratedData gathers and assembles every source relative to
// repoRoot (the directory containing go.mod). It is separated from run so
// llmsgen_test.go can call it directly against the real repo root without
// going through os.Exit.
func buildGeneratedData(repoRoot string) (generatedData, error) {
	modVersions, err := parseGoModVersions(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return generatedData{}, fmt.Errorf("read go.mod: %w", err)
	}

	connectors, err := gatherConnectors(modVersions)
	if err != nil {
		return generatedData{}, fmt.Errorf("gather connectors: %w", err)
	}

	scanned, err := scanRegisteredCodes(repoRoot)
	if err != nil {
		return generatedData{}, fmt.Errorf("scan error codes: %w", err)
	}
	descriptions := make(map[string]string, len(scanned))
	for _, c := range scanned {
		if c.Description != "" {
			descriptions[c.Reason] = c.Description
		}
	}

	return generatedData{
		Connectors: connectors,
		ErrorCodes: gatherErrorCodes(descriptions),
		MCPTools:   gatherMCPTools(),
		Config:     gatherConfigSchema(),
	}, nil
}

// writeGeneratedFile writes normalizeTrailingNewline(content) to path.
func writeGeneratedFile(path, content string) error {
	if err := os.WriteFile(path, []byte(normalizeTrailingNewline(content)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// normalizeTrailingNewline guarantees content ends with exactly one
// trailing newline (design doc D3/AC-8) — content is built entirely with
// "\n" already (render.go never emits "\r\n"), so that is the only
// normalization needed. It is a pure function shared by writeGeneratedFile
// and llmsgen_test.go's TestGenerator_MatchesCommittedFiles, so the
// drift-guard test compares against exactly what a real run would write to
// disk, not an approximation of it.
func normalizeTrailingNewline(content string) string {
	if content == "" || content[len(content)-1] != '\n' {
		content += "\n"
	}
	for len(content) >= 2 && content[len(content)-2] == '\n' {
		content = content[:len(content)-1]
	}
	return content
}
