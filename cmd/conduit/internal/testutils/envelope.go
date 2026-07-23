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

package testutils

import (
	"bytes"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// envelopeSchemaPath resolves cmd/conduit/cecdysis/testdata/envelope.schema.json
// relative to THIS source file's location (recorded by the compiler at build
// time via runtime.Caller), not the test binary's working directory — which
// varies per package under test (`go test` runs with the package directory as
// cwd), so a path relative to that would be wrong for every caller except this
// package itself. This mirrors the well-established Go test-helper pattern for
// loading a fixture that lives outside the calling package's own tree.
//
// This is deliberately NOT a go:embed: go:embed patterns cannot reference a
// parent directory ("../..."), and the schema's canonical home is
// cmd/conduit/cecdysis/testdata (beside the cecdysis.Result type it documents),
// not duplicated into this package's own testdata — see envelope.schema.json's
// own $description for why there must be exactly ONE committed copy.
func envelopeSchemaPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		// Cannot happen for a real, compiled call site — runtime.Caller only
		// fails for a corrupted stack, per its own doc.
		panic("testutils: runtime.Caller(0) failed to resolve this file's own path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "cecdysis", "testdata", "envelope.schema.json")
}

// envelopeSchema is compiled once and reused by every ValidateEnvelope/
// MatchesEnvelope call across the whole test binary.
var envelopeSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(envelopeSchemaPath())
	if err != nil {
		return nil, fmt.Errorf("compile envelope schema: %w", err)
	}
	return sch, nil
})

// ValidateEnvelope reports a non-nil error if b — typically a captured --json
// command's stdout, or a manually assembled cecdysis.Result marshaled to
// JSON — does not conform to the shared Family A envelope schema
// (cmd/conduit/cecdysis/testdata/envelope.schema.json). Every
// cecdysis.CommandWithResult ("Family A") command's test should assert
// ValidateEnvelope(out) == nil on at least one success-path fixture; see
// cmd/conduit/cli/schema_golden_test.go's completeness walk, which requires
// every Family A command to be covered this way.
func ValidateEnvelope(b []byte) error {
	sch, err := envelopeSchema()
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("unmarshal JSON instance: %w", err)
	}
	return sch.Validate(inst) //nolint:wrapcheck // caller only needs pass/fail plus jsonschema's own descriptive error
}

// MatchesEnvelope reports whether b validates against the Family A envelope
// schema, swallowing the specific violation — used by Family B
// (cecdysis.CommandWithExecuteWithClientResult) command tests for the
// negative check: that command's raw protojson/go-json --json output must
// NEVER accidentally start conforming to Family A's envelope shape (a
// command silently drifting into a hybrid shape would be worse than either
// pure shape — see cli-contract.md §4.6's edge-case table).
func MatchesEnvelope(b []byte) bool {
	return ValidateEnvelope(b) == nil
}
