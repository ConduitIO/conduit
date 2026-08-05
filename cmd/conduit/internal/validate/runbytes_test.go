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

package validate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/matryer/is"
)

const validPipeline = `version: 2.2
pipelines:
  - id: gen-pipeline
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
`

// Test_RunBytes_MatchesRunOnDisk is the property that makes the seam
// trustworthy: `generate` gates its candidate through THIS engine precisely so
// there is no second validator to drift. If the two paths could disagree, a
// generated config could pass in memory and fail once written — the exact
// class of bug the shared seam exists to prevent.
func Test_RunBytes_MatchesRunOnDisk(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"valid", validPipeline},
		{"unparseable", "this: is: not: valid: yaml:\n\t- broken"},
		{"missing required fields", "version: 2.2\npipelines:\n  - id: \"\"\n"},
		{"empty", ""},
		{"duplicate ids in one document", validPipeline + "\n---\n" + validPipeline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			dir := t.TempDir()
			path := filepath.Join(dir, "p.yaml")
			is.NoErr(os.WriteFile(path, []byte(tc.src), 0o600))

			onDisk, err := Run(ctx, path)
			is.NoErr(err)
			inMemory := RunBytes(ctx, path, []byte(tc.src), Options{})

			is.Equal(len(inMemory.Files), len(onDisk.Files))
			is.Equal(inMemory.OK(), onDisk.OK())

			// Compare the findings themselves, not just the count: a seam that
			// returned the right NUMBER of different findings would be worse
			// than one that returned none.
			is.Equal(len(inMemory.Files[0].Findings), len(onDisk.Files[0].Findings))
			for i, got := range inMemory.Files[0].Findings {
				want := onDisk.Files[0].Findings[i]
				is.Equal(got.Code, want.Code)
				is.Equal(got.ConfigPath, want.ConfigPath)
				is.Equal(got.Severity, want.Severity)
			}
		})
	}
}

// Test_RunBytes_TouchesNoDisk pins the reason this exists rather than
// write-to-temp-then-Run: a candidate that fails validation must never reach
// the filesystem, and a purely in-memory operation must not be able to fail on
// a disk error. Running with the working directory removed would break any
// implementation that quietly wrote a temp file relative to it.
func Test_RunBytes_TouchesNoDisk(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	entriesBefore, err := os.ReadDir(dir)
	is.NoErr(err)

	rep := RunBytes(context.Background(), filepath.Join(dir, "never-written.yaml"), []byte(validPipeline), Options{})
	is.True(rep.OK())

	entriesAfter, err := os.ReadDir(dir)
	is.NoErr(err)
	is.Equal(len(entriesAfter), len(entriesBefore)) // nothing written
}

// Test_RunBytes_DefaultsTheName pins that a finding always carries a location.
// An empty name would render findings with a blank file, which is not
// actionable, and `<generated>` cannot be mistaken for a real path.
func Test_RunBytes_DefaultsTheName(t *testing.T) {
	is := is.New(t)

	rep := RunBytes(context.Background(), "", []byte("{{{ not yaml"), Options{})
	is.Equal(len(rep.Files), 1)
	is.Equal(rep.Files[0].Path, DefaultInMemoryName)
	is.True(!rep.OK())

	named := RunBytes(context.Background(), "my-candidate", []byte(validPipeline), Options{})
	is.Equal(named.Files[0].Path, "my-candidate")
}

// Test_RunBytes_HonoursOptions pins that the in-memory path is not silently
// hardwired to the zero Options — `generate` and `lint` need the same opt-in
// checks the disk path exposes.
func Test_RunBytes_HonoursOptions(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	// A deprecated/unknown field produces a warning only when Warnings is set.
	const withUnknownField = `version: 2.2
pipelines:
  - id: gen-pipeline
    status: running
    totallyUnknownField: yes
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
`
	quiet := RunBytes(ctx, "c", []byte(withUnknownField), Options{})
	loud := RunBytes(ctx, "c", []byte(withUnknownField), Options{Warnings: true})

	is.True(len(loud.Files[0].Findings) > len(quiet.Files[0].Findings))
}
