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
	//nolint:depguard // testing external error
	"errors"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestWASMProcessor_MalformedProcessor(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := NewWASMProcessor(ctx, zerolog.Nop(), "./wasm_processor_test.go")
	is.True(err != nil)
	is.Equal(err.Error(), "failed running WASM module: failed compiling WASM module: invalid magic number")
}

func TestWASMProcessor_SpecifyError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := NewWASMProcessor(ctx, zerolog.Nop(), testPluginDir+"specify_error/processor.wasm")
	is.NoErr(err)

	_, err = underTest.Specification()
	is.Equal(err, errors.New("boom"))
}

func TestWASMProcessor_Specify(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := NewWASMProcessor(
		ctx,
		zerolog.New(zerolog.NewTestWriter(t)),
		testPluginDir+"chaos_processor/processor.wasm",
	)
	is.NoErr(err)

	gotSpec, err := underTest.Specification()
	is.NoErr(err)

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
	wantSpec := sdk.Specification{
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
	is.Equal(
		gotSpec,
		wantSpec,
	)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := NewWASMProcessor(
		ctx,
		zerolog.New(zerolog.NewTestWriter(t)),
		testPluginDir+"chaos_processor/processor.wasm",
	)
	is.NoErr(err)

	is.NoErr(underTest.Configure(ctx, map[string]string{"process.prefix": "hello!\n\n"}))

	is.NoErr(underTest.Open(ctx))

	processed := underTest.Process(ctx, nil)
	is.Equal(0, len(processed))

	processed = underTest.Process(ctx, []opencdc.Record{})
	is.Equal(0, len(processed))

	input := opencdc.Record{
		Position:  opencdc.Position("first left then right"),
		Operation: opencdc.OperationCreate,
		Metadata: map[string]string{
			"street": "23rd",
		},
		Key: opencdc.RawData("broken"),
		Payload: opencdc.Change{
			After: opencdc.RawData("oranges"),
		},
	}
	want := sdk.SingleRecord(input.Clone())
	want.Payload.After = opencdc.RawData("hello!\n\n" + string(want.Payload.After.Bytes()))
	processed = underTest.Process(ctx, []opencdc.Record{input})
	is.Equal(1, len(processed))
	is.Equal(want, processed[0])

	is.NoErr(underTest.Teardown(ctx))
}
