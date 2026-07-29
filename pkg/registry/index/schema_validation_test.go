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

package index_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// frozenSchemaPath resolves docs/design-documents/registry-index/index-schema.json
// relative to THIS source file (via runtime.Caller), not the test binary's cwd —
// the same pattern cmd/conduit/internal/testutils/envelope.go uses to load a
// fixture living outside the calling package's own tree. The frozen JSON Schema
// is the on-disk/served-shape contract (index-CI linting + fixture testing); the
// R-1 schema doc claims "sample-index.json validates against index-schema.json"
// but nothing in-repo enforced it until this test. PR-A adds processors[] to
// both, so this is now a live regression guard that the two stay in lockstep.
func frozenSchemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	is.New(t).True(ok)
	// pkg/registry/index -> repo root is three parents up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..",
		"docs", "design-documents", "registry-index", "index-schema.json")
}

func compileFrozenSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	is := is.New(t)
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(frozenSchemaPath(t))
	is.NoErr(err)
	return sch
}

func validateAgainstFrozenSchema(t *testing.T, sch *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	is.New(t).NoErr(err)
	return sch.Validate(inst)
}

// TestFrozenSchema_ValidatesSampleIndex is the R-1-doc-promised check, now
// automated: the processor-bearing sample envelope validates against the frozen
// Draft 2020-12 schema. If a future edit adds a field to sample-index.json the
// schema doesn't declare (additionalProperties:false everywhere), or a processor
// entry that violates the arch-neutral (wasip1/wasm/wasm-processor) constraint,
// this fails.
func TestFrozenSchema_ValidatesSampleIndex(t *testing.T) {
	is := is.New(t)
	sch := compileFrozenSchema(t)

	raw, err := os.ReadFile("testdata/sample-index.json")
	is.NoErr(err)

	is.NoErr(validateAgainstFrozenSchema(t, sch, raw))
}

// TestFrozenSchema_ValidatesConnectorOnlyIndex proves the additive change is
// backward-compatible at the schema level: an index with NO processors[] key at
// all (the pre-processor shape) still validates, because processors[] is not in
// payload.required.
func TestFrozenSchema_ValidatesConnectorOnlyIndex(t *testing.T) {
	is := is.New(t)
	sch := compileFrozenSchema(t)

	raw, err := os.ReadFile("testdata/sample-index-connectors-only.json")
	is.NoErr(err)

	is.NoErr(validateAgainstFrozenSchema(t, sch, raw))
}

// TestFrozenSchema_RejectsHostTargetedProcessorArtifact is the schema-level
// counterpart to SelectProcessorArtifact's runtime refusal (design doc D2,
// failure mode 3): a processor artifact carrying a host os/arch instead of the
// fixed wasip1/wasm must be a schema violation, not a valid-but-skipped entry.
// This is what makes "a host os/arch is a malformed/spoofed entry" enforceable
// by index-CI linting, not only at install time.
func TestFrozenSchema_RejectsHostTargetedProcessorArtifact(t *testing.T) {
	is := is.New(t)
	sch := compileFrozenSchema(t)

	raw, err := os.ReadFile("testdata/sample-index.json")
	is.NoErr(err)

	// Rewrite the single processor artifact's os/arch to a host platform.
	var env map[string]any
	is.NoErr(json.Unmarshal(raw, &env))
	payload := env["payload"].(map[string]any)
	procs := payload["processors"].([]any)
	art := procs[0].(map[string]any)["versions"].([]any)[0].(map[string]any)["artifact"].(map[string]any)
	art["os"] = "linux"
	art["arch"] = "amd64"

	mutated, err := json.Marshal(env)
	is.NoErr(err)

	// Must now FAIL: processorArtifact.os is const "wasip1", arch const "wasm".
	is.True(validateAgainstFrozenSchema(t, sch, mutated) != nil)
}

// TestFrozenSchema_RejectsProcessorArtifactsList proves a processor version
// cannot smuggle a connector-shaped per-host "artifacts" array in place of the
// single arch-neutral "artifact" object (design doc D1/D2). additionalProperties
// is false on processorVersion, and "artifact" is required, so an "artifacts"
// key is rejected.
func TestFrozenSchema_RejectsProcessorArtifactsList(t *testing.T) {
	is := is.New(t)
	sch := compileFrozenSchema(t)

	raw, err := os.ReadFile("testdata/sample-index.json")
	is.NoErr(err)

	var env map[string]any
	is.NoErr(json.Unmarshal(raw, &env))
	payload := env["payload"].(map[string]any)
	ver := payload["processors"].([]any)[0].(map[string]any)["versions"].([]any)[0].(map[string]any)
	// Replace the single "artifact" with a connector-style "artifacts" list.
	ver["artifacts"] = []any{ver["artifact"]}
	delete(ver, "artifact")

	mutated, err := json.Marshal(env)
	is.NoErr(err)

	is.True(validateAgainstFrozenSchema(t, sch, mutated) != nil)
}
