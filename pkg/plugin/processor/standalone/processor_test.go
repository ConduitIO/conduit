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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/wasm"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestWASMProcessor_SpecifyError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, SpecifyError)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	_, err = underTest.Specification()
	is.Equal(err, wasm.NewError(0, "boom"))
}

func TestWASMProcessor_Specification(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, ChaosProcessor)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	gotSpec, err := underTest.Specification()
	is.NoErr(err)

	wantSpec := ChaosProcessorSpecifications()
	is.Equal(gotSpec, wantSpec)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, ChaosProcessor)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, nil)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	r, hostModule := NewTestWazeroRuntime(ctx, t)
	procModule, err := r.CompileModule(ctx, ChaosProcessor)
	is.NoErr(err)

	underTest, err := newWASMProcessor(ctx, r, procModule, hostModule, "test-processor", logger)
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
