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

	"github.com/tetratelabs/wazero"
)

// UseInterpreterRuntimeInTests makes every wazero runtime this package creates
// from that point on use wazero's INTERPRETER engine instead of its default
// optimizing compiler (wazevo).
//
// Why this exists: wazevo compiles a WASM module to native machine code.
// Conduit's test processor fixtures are ~19 MB `GOOS=wasip1 go build` binaries,
// and compiling one of those to arm64 costs ~4s — but ~57s under `-race`,
// because ThreadSanitizer instruments wazevo's SSA construction and register
// allocator, which are among the most memory-access-dense code paths in the
// dependency tree (measured: 14x amplification, and >40% of the entire
// pkg/registry test binary's CPU samples). The interpreter needs no codegen and
// costs ~3.6s under `-race` for the same module, while still decoding,
// validating, instantiating and EXECUTING the guest exactly as the compiler
// does — only slower per instruction, which is irrelevant for a test that reads
// a module's Specification() once and throws it away.
//
// This package's own TestMain has made that trade since 2023; this function
// exists so test binaries in OTHER packages that drive this package's loader
// (pkg/registry's install-time WASM validation tests, via InspectSpecification)
// can make it too, instead of each paying a full optimizing-compiler run per
// test.
//
// Conduit itself NEVER calls this. Production keeps the optimizing compiler, so
// install-time validation keeps proving that a fetched artifact codegens under
// the very engine that will later run it — the guarantee InspectSpecification
// was designed to provide. Tests that specifically assert compiler-backend
// behavior must not call this.
//
// Call it from TestMain before any test runs, and never undo it: it writes a
// package-level variable, so it is not safe to call concurrently with plugin
// loading or with itself. The choice is process-wide and set once; each
// NewRegistry/InspectSpecification call still builds and tears down its own
// independent runtime, so no engine state is shared between tests.
func UseInterpreterRuntimeInTests() {
	newRuntime = func(ctx context.Context) wazero.Runtime {
		return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	}
}
