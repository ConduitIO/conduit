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

package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Rule 1 normalizes exactly two values before comparing goldens: the
// Conduit version in report.generator (and the pipeline description that
// repeats it) and the report hash in each pipeline description. Nothing
// else, so a rules or upstream version change is a visible golden diff.
var (
	generatorVersionRe = regexp.MustCompile(`("conduit"\s*:\s*")[^"]*(")`)
	descVersionRe      = regexp.MustCompile(`(by conduit )[^;\s"]+`)
	reportHashRe       = regexp.MustCompile(`sha256:[0-9a-f]{64}`)
)

// Normalize applies rule 1 to one output file.
func Normalize(name string, b []byte) []byte {
	if name == reportFile {
		b = generatorVersionRe.ReplaceAll(b, []byte("${1}<VERSION>${2}"))
	}
	b = descVersionRe.ReplaceAll(b, []byte("${1}<VERSION>"))
	return reportHashRe.ReplaceAll(b, []byte("sha256:<REPORT_SHA256>"))
}

const exitCodeFile = "exit_code"

// CompareGoldens compares the run's files and exit code with the case's
// expected/ directory, byte for byte after Normalize. With update it
// rewrites expected/ instead and reports nothing.
func CompareGoldens(caseDir string, out Output, update bool) ([]string, error) {
	dir := filepath.Join(caseDir, "expected")
	got := map[string][]byte{exitCodeFile: []byte(strconv.Itoa(out.ExitCode) + "\n")}
	for name, b := range out.Files {
		got[name] = Normalize(name, b)
	}
	if update {
		if err := os.RemoveAll(dir); err != nil {
			return nil, cerrors.Errorf("clear expected/: %w", err)
		}
		for name, b := range got {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return nil, cerrors.Errorf("write golden: %w", err)
			}
			if err := os.WriteFile(p, b, 0o600); err != nil {
				return nil, cerrors.Errorf("write golden: %w", err)
			}
		}
		return nil, nil
	}
	want, err := readTree(dir)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return []string{"expected/ is missing: generate the goldens with -update and review the diff"}, nil
	}
	var p []string
	for _, name := range unionKeys(want, got) {
		w, inWant := want[name]
		g, inGot := got[name]
		switch {
		case !inGot:
			p = append(p, fmt.Sprintf("golden %s was not produced", name))
		case !inWant:
			p = append(p, fmt.Sprintf("output %s has no golden", name))
		case !bytes.Equal(w, g):
			p = append(p, fmt.Sprintf("output %s differs from its golden at line %d (re-run with -update and review)", name, firstDiffLine(w, g)))
		}
	}
	return p, nil
}

// CompareRuns is the determinism property: two runs of the same case, from
// different working directories, produce byte-identical output.
func CompareRuns(a, b Output) []string {
	var p []string
	if a.ExitCode != b.ExitCode {
		p = append(p, fmt.Sprintf("exit code %d vs %d between two runs", a.ExitCode, b.ExitCode))
	}
	if !bytes.Equal(a.Stdout, b.Stdout) {
		p = append(p, fmt.Sprintf("--json output differs between two runs at line %d", firstDiffLine(a.Stdout, b.Stdout)))
	}
	for _, name := range unionKeys(a.Files, b.Files) {
		if !bytes.Equal(a.Files[name], b.Files[name]) {
			p = append(p, fmt.Sprintf("%s differs between two runs", name))
		}
	}
	return p
}

func unionKeys(a, b map[string][]byte) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstDiffLine(a, b []byte) int {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl)) + 1
}

// Schema paths, relative to the repository root.
const (
	EnvelopeSchemaPath = "cmd/conduit/cecdysis/testdata/envelope.schema.json"
	// ReportSchemaPath is where #3057 puts the report schema. It does not
	// exist until the CLI lane's first PR; until then schema conformance of
	// the result is pending.
	ReportSchemaPath = "pkg/kcmigrate/schema/report.v1.schema.json"
)

// ValidateSchema validates doc against the JSON Schema at repoRoot/rel. A
// missing schema file is ErrPending.
func ValidateSchema(repoRoot, rel string, doc []byte) error {
	p := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return cerrors.Errorf("%s does not exist: %w", rel, ErrPending)
	}
	sch, err := jsonschema.NewCompiler().Compile(p)
	if err != nil {
		return cerrors.Errorf("compile %s: %w", rel, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return cerrors.Errorf("unmarshal instance: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return cerrors.Errorf("does not conform to %s: %w", rel, err)
	}
	return nil
}

// RepoRoot walks up from dir to the directory holding this module's go.mod.
func RepoRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && bytes.HasPrefix(b, []byte("module github.com/conduitio/conduit\n")) {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", cerrors.New("repository root not found above " + dir)
		}
	}
}
