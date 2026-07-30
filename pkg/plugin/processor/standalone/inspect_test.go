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

package standalone

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// TestInspectSpecification_Chaos is the happy path: a real, compilable
// standalone WASM processor's self-declared Specification is returned exactly
// as a NewRegistry-loaded plugin's would be — proving install-time validation
// (pkg/registry) reads the SAME name/version the runtime will register the
// plugin under. The wasm is built by TestMain (standalone_test.go).
func TestInspectSpecification_Chaos(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	spec, err := InspectSpecification(ctx, log.Nop(), testPluginChaosDir+"processor.wasm")
	is.NoErr(err)
	is.Equal("chaos-processor", spec.Name)
	is.Equal("v1.3.5", spec.Version)
}

// TestInspectSpecification_CompileError refuses a file that is not a valid
// WASM module at all (the malformed fixture is a plain .txt) — the compile step
// fails and the error is surfaced, never a zero-value spec silently returned.
func TestInspectSpecification_CompileError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := InspectSpecification(ctx, log.Nop(), testPluginMalformedDir+"processor.txt")
	is.True(err != nil)
}

// TestInspectSpecification_SpecifyError refuses a module that compiles but
// whose Specification() returns an error ("boom") — the spec-extraction failure
// propagates, exactly as it does in NewRegistry's discovery.
func TestInspectSpecification_SpecifyError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := InspectSpecification(ctx, log.Nop(), testPluginSpecifyErrorDir+"processor.wasm")
	is.True(err != nil)
}

// TestInspectSpecification_MissingFile refuses a path that does not exist,
// rather than panicking or returning a zero spec.
func TestInspectSpecification_MissingFile(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := InspectSpecification(ctx, log.Nop(), testPluginChaosDir+"does-not-exist.wasm")
	is.True(err != nil)
}
