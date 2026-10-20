// Copyright © 2023 Meroxa, Inc.
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
	"fmt"
	"maps"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/internal/wasmengine"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	testPluginDir = "./test/wasm_processors/"

	testPluginChaosDir        = testPluginDir + "chaos/"
	testPluginEchoDir         = testPluginDir + "echo/"
	testPluginMalformedDir    = testPluginDir + "malformed/"
	testPluginOldEchoDir      = testPluginDir + "old_echo/"
	testPluginSpecifyErrorDir = testPluginDir + "specify_error/"
	testPluginStallDir        = testPluginDir + "stall/"
	testPluginSpinDir         = testPluginDir + "spin/"
)

var (
	// TestRuntime can be reused in tests to avoid recompiling the test modules
	TestRuntime        wazero.Runtime
	CompiledHostModule *wazergo.CompiledModule[*hostModuleInstance]

	ChaosProcessorBinary     []byte
	EchoProcessorBinary      []byte
	MalformedProcessorBinary []byte
	OldEchoProcessorBinary   []byte
	SpecifyErrorBinary       []byte
	StallBinary              []byte
	SpinBinary               []byte

	ChaosProcessorModule wazero.CompiledModule
	SpecifyErrorModule   wazero.CompiledModule
	StallModule          wazero.CompiledModule
	SpinModule           wazero.CompiledModule

	testProcessorPaths = map[string]tuple[*[]byte, *wazero.CompiledModule]{
		testPluginChaosDir + "processor.wasm": {&ChaosProcessorBinary, &ChaosProcessorModule},
		// The echo guests are read here but compiled lazily by the tests that use
		// them (compileForTest): each ~25 MB module costs ~1.6 GB of heap to
		// compile under -race, and compiling every guest in TestMain pushed the
		// package past the CI runner's memory (swap thrash, runner killed).
		testPluginEchoDir + "processor.wasm":         {&EchoProcessorBinary, nil},
		testPluginMalformedDir + "processor.txt":     {&MalformedProcessorBinary, nil},
		testPluginOldEchoDir + "processor.wasm":      {&OldEchoProcessorBinary, nil},
		testPluginSpecifyErrorDir + "processor.wasm": {&SpecifyErrorBinary, &SpecifyErrorModule},
		testPluginStallDir + "processor.wasm":        {&StallBinary, &StallModule},
		testPluginSpinDir + "processor.wasm":         {&SpinBinary, &SpinModule},
	}
)

func TestMain(m *testing.M) {
	exitOnError := func(err error, msg string) {
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v: %v", msg, err)
			os.Exit(1)
		}
	}

	cmd := exec.CommandContext(context.Background(), "bash", "./test/build-test-processors.sh")

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	{
		fmt.Printf("Building test processors (%s)...\n", cmd.String())
		start := time.Now()

		err := cmd.Run()
		exitOnError(err, "error executing bash script")

		fmt.Printf("Built test processors in %v\n", time.Since(start))
	}

	// instantiate shared test runtime
	ctx := context.Background()

	// Use the interpreter runtime as it's faster for tests; set
	// CONDUIT_TEST_WASM_ENGINE=compiler to run this binary against wazero's
	// optimizing compiler instead. See wasmengine.ConfigureForTests.
	wasmengine.ConfigureForTests()

	// Close-on-context-done on: the spin-guest kill test needs it (it is how the
	// install-time runtime is built). Throughput is irrelevant to these tests.
	TestRuntime = newRuntime(ctx, true)

	_, err := wasi_snapshot_preview1.Instantiate(ctx, TestRuntime)
	exitOnError(err, "error instantiating WASI")

	CompiledHostModule, err = wazergo.Compile(ctx, TestRuntime, hostModule)
	exitOnError(err, "error compiling host module")

	// load test processors
	for path, t := range testProcessorPaths {
		*t.V1, err = os.ReadFile(path)
		exitOnError(err, "error reading file "+path)

		if t.V2 == nil {
			continue
		}

		fmt.Printf("Compiling module %s...\n", path)
		start := time.Now()

		// note that modules can't be compiled in parallel, because the runtime
		// is not thread-safe
		*t.V2, err = TestRuntime.CompileModule(ctx, *t.V1)
		exitOnError(err, "error compiling module "+path)

		fmt.Printf("Compiled module %s in %v\n", path, time.Since(start))
	}

	// run tests
	code := m.Run()

	err = TestRuntime.Close(ctx)
	exitOnError(err, "error closing wasm runtime")

	os.Exit(code)
}

func ChaosProcessorSpecifications() sdk.Specification {
	param := config.Parameter{
		Default:     "success",
		Type:        config.ParameterTypeString,
		Description: "prefix",
		Validations: []config.Validation{
			config.ValidationInclusion{List: []string{"success", "error", "panic"}},
		},
	}

	dummyProcessor := sdk.NewProcessorFunc(sdk.Specification{}, nil)
	spec, err := sdk.ProcessorWithMiddleware(dummyProcessor, sdk.DefaultProcessorMiddleware()...).Specification()
	if err != nil {
		panic(cerrors.Errorf("failed to get specifications for middleware: %w", err))
	}

	chaosParams := map[string]config.Parameter{
		"configure": param,
		"open":      param,
		"process.prefix": {
			Default:     "",
			Type:        config.ParameterTypeString,
			Description: "prefix to be added to the payload's after",
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		"process":  param,
		"teardown": param,
	}
	// add parameters from middleware
	maps.Copy(chaosParams, spec.Parameters)

	return sdk.Specification{
		Name:        "chaos-processor",
		Summary:     "chaos processor summary",
		Description: "chaos processor description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters:  chaosParams,
	}
}
