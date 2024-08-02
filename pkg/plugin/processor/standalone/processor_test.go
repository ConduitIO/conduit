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

	"github.com/google/go-cmp/cmp"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestWASMProcessor_Specification_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	gotSpec, err := underTest.Specification()
	is.NoErr(err)

	wantSpec := ChaosProcessorSpecifications()
	is.Equal("", cmp.Diff(gotSpec, wantSpec))

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Specification_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, SpecifyErrorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	_, err = underTest.Specification()
	is.Equal(err, pprocutils.NewError(0, "boom"))

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, nil)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"configure": "error"})
	is.Equal(err, pprocutils.NewError(0, "boom"))

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure_Panic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"configure": "panic"})
	is.True(cerrors.Is(err, plugin.ErrPluginNotRunning))

	// Teardown should also fail with the same error
	err = underTest.Teardown(ctx)
	is.True(cerrors.Is(err, plugin.ErrPluginNotRunning))
}

func TestWASMProcessor_Open_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Open_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"open": "error"})
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.Equal(err, pprocutils.NewError(0, "boom"))

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Open_Panic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"open": "panic"})
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.True(cerrors.Is(err, plugin.ErrPluginNotRunning))

	// Teardown should also fail with the same error
	err = underTest.Teardown(ctx)
	is.True(cerrors.Is(err, plugin.ErrPluginNotRunning))
}

func TestWASMProcessor_Process_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	is.NoErr(underTest.Configure(ctx, map[string]string{"process.prefix": "hello!\n\n"}))

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

func TestWASMProcessor_Process_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	is.NoErr(underTest.Configure(ctx, map[string]string{"process": "error"}))

	processed := underTest.Process(ctx, nil)
	is.Equal(1, len(processed))

	errRecord, ok := processed[0].(sdk.ErrorRecord)
	is.True(ok)
	is.Equal(errRecord.Error, pprocutils.NewError(0, "boom"))

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Process_Panic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	is.NoErr(underTest.Configure(ctx, map[string]string{"process": "panic"}))

	processed := underTest.Process(ctx, nil)
	is.Equal(1, len(processed))

	errRecord, ok := processed[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(cerrors.Is(errRecord.Error, plugin.ErrPluginNotRunning))

	// Teardown should also fail with the same error
	err = underTest.Teardown(ctx)
	is.True(cerrors.Is(err, plugin.ErrPluginNotRunning))
}

func TestWASMProcessor_Configure_Schema_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"configure": "create_and_get_schema"})
	is.NoErr(err)

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}
