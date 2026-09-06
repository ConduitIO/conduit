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

// Package wasmengine owns the process-wide choice of wazero engine used to
// compile and run standalone WASM processors.
//
// It exists to hold exactly one mutable knob — which wazero engine
// pkg/plugin/processor/standalone constructs its runtimes with — somewhere test
// binaries in several packages can reach but nothing outside this module can.
// Conduit is an embeddable library, so an exported hook in a non-internal
// package would let a downstream importer silently downgrade the production
// WASM engine; living under pkg/internal makes that a compile error.
//
// The knob is deliberately process-wide rather than plumbed through
// NewRegistry/InspectSpecification: it is a test-harness concern, not a Conduit
// configuration option, and giving it a config surface would invite exactly the
// production misuse this package is shaped to prevent.
package wasmengine

import (
	"context"
	"os"
	"testing"

	"github.com/tetratelabs/wazero"
)

const (
	// EngineEnvVar selects the wazero engine a TEST binary runs against. It is
	// read only by ConfigureForTests and has no effect on Conduit itself.
	EngineEnvVar = "CONDUIT_TEST_WASM_ENGINE"

	// EngineCompiler is the EngineEnvVar value that opts a test binary back
	// into wazero's optimizing compiler.
	EngineCompiler = "compiler"
)

// New constructs the wazero runtime used to compile and run standalone WASM
// processors. It is a variable so test binaries can swap the engine via
// ConfigureForTests; production always leaves it at wazero.NewRuntime.
//
// wazero.NewRuntime selects the engine automatically: the wazevo optimizing
// compiler where the host architecture supports it (amd64/arm64 on mainstream
// OSes) and the interpreter everywhere else. So "production uses the compiler"
// is a per-host statement, not a universal one — Conduit's linux/386 build, for
// instance, already runs the interpreter in production.
var New = wazero.NewRuntime

// ConfigureForTests picks the wazero engine for the calling TEST binary. By
// default it swaps in wazero's INTERPRETER; setting CONDUIT_TEST_WASM_ENGINE=compiler
// leaves wazero's default (usually the wazevo optimizing compiler) in place.
//
// Why the interpreter is the default: wazevo generates native machine code.
// Conduit's standalone processor fixtures are ~19 MB
// `GOOS=wasip1 GOARCH=wasm go build` binaries, and compiling one to arm64 costs
// ~4s — but ~57s under `-race`, because ThreadSanitizer instruments wazevo's
// SSA construction and register allocator, among the most memory-access-dense
// code paths in the dependency tree (measured: 14x amplification, and >40% of
// the pkg/registry test binary's CPU samples). The interpreter needs no codegen
// and costs ~3.6s under `-race` for the same module, while still decoding,
// validating, instantiating and EXECUTING the guest — only slower per
// instruction, which is irrelevant to a test that reads a module's
// Specification() once and throws it away.
//
// Why the compiler escape hatch exists: with the interpreter selected, nothing
// in the calling binary exercises wazevo, so a wazevo regression (a
// tetratelabs/wazero bump, say) would not be caught there. Setting the env var
// restores that coverage — run it WITHOUT `-race`, where the compile costs ~4s
// rather than ~57s.
//
// Scope, stated precisely: this swaps the engine for BOTH the throwaway runtime
// standalone.InspectSpecification builds AND the runtime a live
// standalone.Registry compiles and runs every processor with. It is not scoped
// to install-time validation.
//
// It panics outside a test binary. A production caller would silently downgrade
// the WASM engine, and — because the write is unsynchronized against the reads
// in standalone's NewRegistry/InspectSpecification — would also introduce a
// data race. Call it from TestMain, before any test runs, and never undo it.
func ConfigureForTests() {
	if !testing.Testing() {
		panic("wasmengine: ConfigureForTests called outside a test binary; " +
			"this would downgrade the production WASM engine to the interpreter")
	}
	if os.Getenv(EngineEnvVar) == EngineCompiler {
		return
	}
	New = func(ctx context.Context) wazero.Runtime {
		return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	}
}
