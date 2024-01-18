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
	"os"
	"os/exec"
	"testing"

	sdk "github.com/conduitio/conduit-processor-sdk"

	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/matryer/is"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	//	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	testPluginDir = "./test/wasm_processors/"

	testPluginChaosDir        = testPluginDir + "chaos/"
	testPluginMalformedDir    = testPluginDir + "malformed/"
	testPluginSpecifyErrorDir = testPluginDir + "specify_error/"
)

var (
	ChaosProcessor     []byte
	MalformedProcessor []byte
	SpecifyError       []byte

	testProcessorPaths = map[string]*[]byte{
		testPluginChaosDir + "processor.wasm":        &ChaosProcessor,
		testPluginMalformedDir + "processor.txt":     &MalformedProcessor,
		testPluginSpecifyErrorDir + "processor.wasm": &SpecifyError,
	}
)

func TestMain(m *testing.M) {
	exitOnError := func(err error, msg string) {
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v: %v", msg, err)
			os.Exit(1)
		}
	}

	cmd := exec.Command("bash", "./test/build-test-processors.sh")

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitOnError(err, "error executing bash script")

	// load test processors
	for path, target := range testProcessorPaths {
		*target, err = os.ReadFile(path)
		exitOnError(err, "error reading file "+path)
	}

	os.Exit(m.Run())
}

func NewTestWazeroRuntime(ctx context.Context, t *testing.T) (wazero.Runtime, *wazergo.CompiledModule[*hostModuleInstance]) {
	is := is.New(t)

	r := wazero.NewRuntime(ctx)
	t.Cleanup(func() {
		err := r.Close(ctx)
		is.NoErr(err)
	})

	_, err := wasi_snapshot_preview1.Instantiate(ctx, r)
	is.NoErr(err)

	m, err := wazergo.Compile(ctx, r, hostModule)
	is.NoErr(err)

	return r, m
}

func ChaosProcessorSpecifications() sdk.Specification {
	param := sdk.Parameter{
		Default:     "success",
		Type:        sdk.ParameterTypeString,
		Description: "prefix",
		Validations: []sdk.Validation{
			{
				Type:  sdk.ValidationTypeInclusion,
				Value: "success,error,panic",
			},
		},
	}
	return sdk.Specification{
		Name:        "chaos-processor",
		Summary:     "chaos processor summary",
		Description: "chaos processor description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"configure": param,
			"open":      param,
			"process.prefix": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "prefix to be added to the payload's after",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
			"process":  param,
			"teardown": param,
		},
	}
}
