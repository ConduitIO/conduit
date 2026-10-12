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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestWASMProcessor_Specification_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, SpecifyErrorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, nil)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Configure_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.NoErr(err)

	is.NoErr(underTest.Teardown(ctx))
}

func TestWASMProcessor_Open_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
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

	underTest, err := newWASMProcessor(ctx, TestRuntime, ChaosProcessorModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", logger)
	is.NoErr(err)

	err = underTest.Configure(ctx, map[string]string{"configure": "create_and_get_schema"})
	is.NoErr(err)

	// Teardown still works
	is.NoErr(underTest.Teardown(ctx))
}

// shrinkModuleStopTimeout shrinks the module stop window so the kill-path test is fast.
// It writes a package-level variable, so tests using it must not be parallel.
func shrinkModuleStopTimeout(t *testing.T, stop time.Duration) {
	t.Helper()
	old := moduleStopTimeout
	moduleStopTimeout = stop
	t.Cleanup(func() { moduleStopTimeout = old })
}

// cancelOnceGuestAccepts interposes a forwarding channel in front of the
// guest's command channel and calls onAccepted right after the guest has taken
// a request. The test uses it to expire a command's context at exactly the
// moment the guest owns the request, with no sleeps or timing assumptions: the
// stall guest then cannot have answered yet, so the host must be the one to give
// up. (Expiring the context any earlier means the request was never sent, which
// leaves nothing outstanding and so does not exercise the bug.)
//
// closeModule closes p.commandRequests; the forwarder propagates that to the
// real channel so the guest still sees "no more commands".
func cancelOnceGuestAccepts(p *wasmProcessor, onAccepted func()) {
	guest := p.commandRequests
	proxy := make(chan *processorv1.CommandRequest)
	p.commandRequests = proxy
	go func() {
		defer close(guest)
		for req := range proxy {
			select {
			case guest <- req:
				onAccepted()
			case <-p.moduleStopped:
				return
			}
		}
	}()
}

func specifyRequest() *processorv1.CommandRequest {
	return &processorv1.CommandRequest{Request: &processorv1.CommandRequest_Specify{Specify: &processorv1.Specify_Request{}}}
}

// teardownWithin runs Teardown and fails the test, instead of hanging it, if it
// does not return within guard. The guard only has to be finite and far longer
// than a correct Teardown; the assertion is that Teardown returns at all.
func teardownWithin(t *testing.T, p *wasmProcessor, guard time.Duration) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- p.Teardown(context.Background()) }()
	select {
	case err := <-errCh:
		return err
	case <-time.After(guard):
		t.Fatalf("Teardown still blocked after %v: abandoned command left the guest wedged (#2981)", guard)
		return nil
	}
}

// TestWASMProcessor_AbandonedCommand_TeardownReturns is the regression test for
// the TestInstallProcessor_Idempotent hang (#2981). The guest accepts Specify and
// is busy for ~3s; the host gives up first. When the guest then delivers its
// response on the unbuffered response channel nobody reads it, so it used to
// block inside command_response forever, never reached command_request again,
// and Teardown (no timeout, uncancellable context) blocked sending its request
// until the test binary's 10m alarm fired.
func TestWASMProcessor_AbandonedCommand_TeardownReturns(t *testing.T) {
	is := is.New(t)

	// log.Nop: the guest outlives the test body by a few seconds and its
	// goroutine logs when it stops; a t-bound logger would panic after the test.
	underTest, err := newWASMProcessor(context.Background(), TestRuntime, StallModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Nop())
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceGuestAccepts(underTest, cancel)

	_, err = underTest.executeCommand(ctx, specifyRequest())
	is.True(cerrors.Is(err, ErrCommandTimeout))
	is.True(cerrors.Is(err, context.Canceled))

	// Commands after an abandoned one fail fast: the late response could
	// otherwise be paired with them (no correlation id in the protocol).
	err = underTest.Configure(context.Background(), nil)
	is.True(cerrors.Is(err, ErrCommandTimeout))

	// The guest wakes after ~3s, its response is dropped, it sees the closed
	// command channel and exits, so Teardown completes without a kill.
	err = teardownWithin(t, underTest, 60*time.Second)
	is.True(cerrors.Is(err, ErrCommandTimeout)) // the skipped teardown command is reported
	is.True(!strings.Contains(err.Error(), "did not stop within"))
}

// TestWASMProcessor_StuckModule_TeardownKills covers a guest that does not come
// back within the stop window: Teardown must kill it and return, not wait.
func TestWASMProcessor_StuckModule_TeardownKills(t *testing.T) {
	is := is.New(t)
	shrinkModuleStopTimeout(t, 200*time.Millisecond)

	underTest, err := newWASMProcessor(context.Background(), TestRuntime, StallModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Nop())
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceGuestAccepts(underTest, cancel)

	_, err = underTest.executeCommand(ctx, specifyRequest())
	is.True(cerrors.Is(err, ErrCommandTimeout))

	// The guest is asleep for ~3s, far beyond the 200ms stop window.
	err = teardownWithin(t, underTest, 60*time.Second)
	is.True(cerrors.Is(err, ErrCommandTimeout))
	is.True(strings.Contains(err.Error(), "did not stop within"))
}

// TestWASMProcessor_Specification_HonorsCallerContext: a caller deadline shorter
// than specificationTimeout bounds the command (the install path passes its
// context through loadBlueprint).
func TestWASMProcessor_Specification_HonorsCallerContext(t *testing.T) {
	is := is.New(t)

	underTest, err := newWASMProcessor(context.Background(), TestRuntime, StallModule, CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Nop())
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceGuestAccepts(underTest, cancel)

	_, err = underTest.specification(ctx)
	is.True(cerrors.Is(err, ErrCommandTimeout))

	_ = teardownWithin(t, underTest, 60*time.Second)
}
