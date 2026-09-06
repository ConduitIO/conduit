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

package registry_test

import (
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/internal/wasmengine"
)

// TestMain picks this test binary's WASM engine, making the same trade
// pkg/plugin/processor/standalone's own TestMain has made since 2023 ("use
// interpreter runtime as it's faster for tests").
//
// InstallProcessor's install-time validation step (validateProcessorWASM ->
// standalone.InspectSpecification) compiles the fetched artifact with a real
// wazero runtime. Under `-race`, wazero's optimizing compiler (wazevo) takes
// ~57s to generate arm64 machine code for the ~19 MB
// `GOOS=wasip1 go build` test fixture — ~4s of actual work amplified 14x by
// ThreadSanitizer instrumenting wazevo's SSA construction and register
// allocator. Five tests in this package install a real processor, so the binary
// paid that ~57s five times: 320s of the package's 401s.
//
// The interpreter needs no codegen and costs ~3.6s for the same module under
// `-race`, while still decoding, validating, instantiating and EXECUTING the
// guest module. Nothing these tests assert — that a genuine module's
// Specification() is read, that its name/version are checked against the index,
// that refusals happen before the atomic rename, that manifest/audit/staging
// bookkeeping is correct — depends on which wazero engine produced the code.
// Set CONDUIT_TEST_WASM_ENGINE=compiler to run this binary against wazevo
// anyway (do it without `-race`, where the compile costs ~4s).
//
// Deliberately NOT done here: sharing one runtime or one compiled module across
// tests. Every InspectSpecification call still builds and tears down its own
// wazero runtime, so no engine state can leak from one test into another; only
// the engine *choice* is process-wide, and it is set once, before any test runs.
func TestMain(m *testing.M) {
	wasmengine.ConfigureForTests()
	os.Exit(m.Run())
}
