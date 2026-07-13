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
	"bufio"
	"fmt"
	"os"
	"strings"
)

// moduleVersions maps a Go module import path to its pinned version string,
// as read from a go.mod file.
type moduleVersions map[string]string

// parseGoModVersions reads modPath (a go.mod file) and returns every
// module's pinned version from its require directives, with any matching
// replace directive applied on top (matching go's own precedence: a
// replace always wins over the require it replaces).
//
// This is a minimal, dependency-free parser scoped to exactly what the
// generator needs (an exact module path's pinned version string), not a
// general go.mod editor — go.mod's grammar has more shapes (build lists,
// exclude, retract, go/toolchain directives) this deliberately does not
// handle, because the generator never needs them.
//
// Determinism (design doc D2/D3): this reads a file committed to the repo,
// never runtime/debug.BuildInfo (which is empty under `go generate`/`go
// run`/`go test` and only populated in a built binary — the design doc's
// call-out for why connector versions cannot come from build info). The
// result is therefore identical across `go generate`, `go run`, `go test`,
// and a built binary, in any environment, as long as go.mod is unchanged.
func parseGoModVersions(modPath string) (moduleVersions, error) {
	f, err := os.Open(modPath)
	if err != nil {
		return nil, fmt.Errorf("open go.mod: %w", err)
	}
	defer f.Close()

	versions := make(moduleVersions)
	replacements := make(moduleVersions)

	var inRequireBlock, inReplaceBlock bool

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		trimmed := strings.TrimSpace(stripLineComment(sc.Text()))
		if trimmed == "" {
			continue
		}

		switch trimmed {
		case "require (":
			inRequireBlock = true
			continue
		case "replace (":
			inReplaceBlock = true
			continue
		case ")":
			inRequireBlock = false
			inReplaceBlock = false
			continue
		}

		switch {
		case inRequireBlock:
			parseRequireLine(trimmed, versions)
		case strings.HasPrefix(trimmed, "require "):
			parseRequireLine(strings.TrimPrefix(trimmed, "require "), versions)
		case inReplaceBlock:
			parseReplaceLine(trimmed, replacements)
		case strings.HasPrefix(trimmed, "replace "):
			parseReplaceLine(strings.TrimPrefix(trimmed, "replace "), replacements)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan go.mod: %w", err)
	}

	for path, version := range replacements {
		versions[path] = version
	}
	return versions, nil
}

// stripLineComment removes a trailing "// ..." comment (e.g. "// indirect")
// from a go.mod line. It does not need to handle "//" inside a quoted
// string: no go.mod line this parser cares about (require/replace) ever
// contains one.
func stripLineComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}

// parseRequireLine parses "path version" (comment already stripped) into
// versions. Malformed or unrecognized lines are silently skipped: this
// parser only needs to resolve the small, known set of connector module
// paths D2 cares about, not validate go.mod's full grammar.
func parseRequireLine(line string, versions moduleVersions) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	versions[fields[0]] = fields[1]
}

// parseReplaceLine parses "old[ oldversion] => new newversion" into
// replacements. A local filesystem replace ("=> ./local/path", no version)
// has no deterministic semver to report and is skipped.
func parseReplaceLine(line string, replacements moduleVersions) {
	parts := strings.SplitN(line, "=>", 2)
	if len(parts) != 2 {
		return
	}
	oldFields := strings.Fields(parts[0])
	if len(oldFields) == 0 {
		return
	}
	oldPath := oldFields[0]

	newFields := strings.Fields(parts[1])
	if len(newFields) < 2 {
		return // local filesystem replace, no version
	}
	replacements[oldPath] = newFields[len(newFields)-1]
}
