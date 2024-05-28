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

//nolint:dogsled // this is a test file
package connector

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/cplugin"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	mock "github.com/conduitio/conduit/pkg/plugin/connector/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// AcceptanceTestV1 is the acceptance test that all implementations of v1
// plugins should pass. It should manually be called from a test case in each
// implementation:
//
//	func TestPlugin(t *testing.T) {
//	    testDispenser := func() {...}
//	    plugin.AcceptanceTestV1(t, testDispenser)
//	}
func AcceptanceTestV1(t *testing.T, tdf testDispenserFunc) {
	// specifier tests
	run(t, tdf, testSpecifier_Specify_Success)
	run(t, tdf, testSpecifier_Specify_Fail)

	// source tests
	run(t, tdf, testSource_Configure_Success)
	run(t, tdf, testSource_Configure_Fail)
	run(t, tdf, testSource_Open_WithPosition)
	run(t, tdf, testSource_Open_EmptyPosition)
	run(t, tdf, testSource_Run_Success)
	run(t, tdf, testSource_Run_Fail)
	run(t, tdf, testSource_Stream_WithoutRun)
	run(t, tdf, testSource_StreamRecv_AfterStop)
	run(t, tdf, testSource_StreamRecv_CancelContext)
	run(t, tdf, testSource_StreamSend_Success)
	run(t, tdf, testSource_Teardown_Success)
	run(t, tdf, testSource_Teardown_Fail)
	run(t, tdf, testSource_Lifecycle_OnCreated)
	run(t, tdf, testSource_Lifecycle_OnUpdated)
	run(t, tdf, testSource_Lifecycle_OnDeleted)
	run(t, tdf, testSource_BlockingFunctions)

	// destination tests
	// run(t, tdf, testDestination_Configure_Success)
	// run(t, tdf, testDestination_Configure_Fail)
	// run(t, tdf, testDestination_Open_Success)
	// run(t, tdf, testDestination_Open_Fail)
	// run(t, tdf, testDestination_Write_Success)
	// run(t, tdf, testDestination_Write_WithoutRun)
	// run(t, tdf, testDestination_Ack_Success)
	// run(t, tdf, testDestination_Ack_WithError)
	// run(t, tdf, testDestination_Ack_WithoutRun)
	// run(t, tdf, testDestination_Run_Fail)
	// run(t, tdf, testDestination_Teardown_Success)
	// run(t, tdf, testDestination_Lifecycle_OnCreated)
	// run(t, tdf, testDestination_Lifecycle_OnUpdated)
	// run(t, tdf, testDestination_Lifecycle_OnDeleted)
	// run(t, tdf, testDestination_BlockingFunctions)
}

func run(t *testing.T, tdf testDispenserFunc, test func(*testing.T, testDispenserFunc)) {
	name := runtime.FuncForPC(reflect.ValueOf(test).Pointer()).Name()
	name = name[strings.LastIndex(name, ".")+1:]
	t.Run(name, func(t *testing.T) { test(t, tdf) })
}

type testDispenserFunc func(*testing.T) (Dispenser, *mock.MockSpecifierPlugin, *mock.MockSourcePlugin, *mock.MockDestinationPlugin)

// ---------------
// -- SPECIFIER --
// ---------------

func testSpecifier_Specify_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := cplugin.SpecifierSpecifyResponse{
		Specification: cplugin.Specification{
			Name:        "test-name",
			Summary:     "A short summary",
			Description: "A long description",
			Version:     "v1.2.3",
			Author:      "Donald Duck",
			SourceParams: config.Parameters{
				"param1.1": {Default: "foo", Type: config.ParameterTypeString, Description: "Param 1.1 description", Validations: nil},
				"param1.2": {Default: "bar", Type: config.ParameterTypeString, Description: "Param 1.2 description", Validations: []config.Validation{config.ValidationRequired{}}},
			},
			DestinationParams: config.Parameters{
				"param2.1": {Default: "baz", Type: config.ParameterTypeString, Description: "Param 2.1 description", Validations: nil},
				"param2.2": {Default: "qux", Type: config.ParameterTypeString, Description: "Param 2.2 description", Validations: []config.Validation{config.ValidationRequired{}}},
			},
		},
	}

	mockSpecifier.EXPECT().
		Specify(gomock.Any(), cplugin.SpecifierSpecifyRequest{}).
		Return(want, nil)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	got, err := specifier.Specify(ctx, cplugin.SpecifierSpecifyRequest{})
	is.NoErr(err)

	is.Equal("", cmp.Diff(want, got))
}

func testSpecifier_Specify_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := cerrors.New("specify error")
	mockSpecifier.EXPECT().
		Specify(gomock.Any(), cplugin.SpecifierSpecifyRequest{}).
		Return(cplugin.SpecifierSpecifyResponse{}, want)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	_, got := specifier.Specify(ctx, cplugin.SpecifierSpecifyRequest{})
	is.Equal(got.Error(), want.Error())
}

// ------------
// -- SOURCE --
// ------------

func testSource_Configure_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	mockSource.EXPECT().
		Configure(gomock.Any(), cplugin.SourceConfigureRequest{Config: nil}).
		Return(cplugin.SourceConfigureResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	got, err := source.Configure(ctx, cplugin.SourceConfigureRequest{Config: map[string]string{}})
	is.NoErr(err)
	is.Equal(got, cplugin.SourceConfigureResponse{})
}

func testSource_Configure_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	cfg := map[string]string{
		"foo":   "bar",
		"empty": "",
	}
	want := cerrors.New("init error")
	mockSource.EXPECT().
		Configure(gomock.Any(), cplugin.SourceConfigureRequest{Config: cfg}).
		Return(cplugin.SourceConfigureResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, got := source.Configure(ctx, cplugin.SourceConfigureRequest{Config: cfg})
	is.Equal(got.Error(), want.Error())
}

func testSource_Open_WithPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	pos := opencdc.Position("test-position")

	mockSource.EXPECT().
		Open(gomock.Any(), cplugin.SourceOpenRequest{Position: pos}).
		Return(cplugin.SourceOpenResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, err = source.Open(ctx, cplugin.SourceOpenRequest{Position: pos})
	is.NoErr(err)
}

func testSource_Open_EmptyPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	mockSource.EXPECT().
		Open(gomock.Any(), cplugin.SourceOpenRequest{Position: nil}).
		Return(cplugin.SourceOpenResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.Open(ctx, cplugin.SourceOpenRequest{Position: nil})
	is.NoErr(err)
	is.Equal(resp, cplugin.SourceOpenResponse{})
}

func testSource_Run_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	var want []opencdc.Record
	for i := 0; i < 10; i++ {
		want = append(want, opencdc.Record{
			Position:  opencdc.Position(fmt.Sprintf("test-position-%d", i)),
			Operation: opencdc.OperationCreate,
			Metadata: map[string]string{
				"foo":   "bar",
				"empty": "",
			},
			Key: opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				Before: nil,
				After:  opencdc.RawData("test-payload"),
			},
		})
	}

	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cplugin.SourceRunStream) error {
			serverStream := stream.Server()
			for _, r := range want {
				err := serverStream.Send(cplugin.SourceRunResponse{Records: []opencdc.Record{r}})
				if err != nil {
					return err
				}
			}
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	var got []opencdc.Record
	clientStream := stream.Client()
	for i := 0; i < len(want); i++ {
		rec, err := clientStream.Recv()
		is.NoErr(err)
		got = append(got, rec.Records...)
	}

	is.Equal("", cmp.Diff(want, got, cmpopts.IgnoreUnexported(opencdc.Record{})))
}

func testSource_Stream_WithoutRun(t *testing.T, tdf testDispenserFunc) {
	t.Skip("TODO: this test panics, we should probably return an error")

	is := is.New(t)
	dispenser, _, _, _ := tdf(t)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	// TODO this panics, should we return an error?
	clientStream := stream.Client()
	_, err = clientStream.Recv()
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_StreamRecv_AfterStop(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cplugin.SourceStopResponse{
		LastPosition: []byte("foo"),
	}

	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, cplugin.SourceRunStream) error {
			<-stopRunCh
			return nil
		})
	mockSource.EXPECT().
		Stop(gomock.Any(), cplugin.SourceStopRequest{}).
		DoAndReturn(func(context.Context, cplugin.SourceStopRequest) (cplugin.SourceStopResponse, error) {
			close(stopRunCh)
			return want, nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	got, err := source.Stop(ctx, cplugin.SourceStopRequest{})
	is.NoErr(err)
	is.Equal(got, want)

	_, err = clientStream.Recv()
	is.True(cerrors.Is(err, ErrStreamNotOpen))

	select {
	case <-stopRunCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Stop")
	}
}

func testSource_StreamRecv_CancelContext(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cplugin.SourceRunStream) error {
			<-stopRunCh
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	runCtx, runCancel := context.WithCancel(ctx)

	stream := source.NewStream()
	err = source.Run(runCtx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	// calling read when source didn't produce records should block until start
	// ctx is cancelled
	time.AfterFunc(time.Millisecond*50, func() {
		runCancel()
	})

	_, err = clientStream.Recv()
	is.True(cerrors.Is(err, context.Canceled))

	close(stopRunCh) // stop run channel
}

func testSource_StreamSend_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := opencdc.Position("test-position")

	// Function Source.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cplugin.SourceRunStream) error {
			defer close(closeCh)
			serverStream := stream.Server()
			got, err := serverStream.Recv()
			is.NoErr(err)
			is.Equal(len(got.AckPositions), 1)
			is.Equal("", cmp.Diff(got.AckPositions[0], want))
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	err = clientStream.Send(cplugin.SourceRunRequest{AckPositions: []opencdc.Position{want}})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Ack")
	}

	// wait for stream closing to propagate from plugin to Conduit
	time.Sleep(time.Millisecond * 50)

	// acking after the stream is closed should result in an error
	err = clientStream.Send(cplugin.SourceRunRequest{AckPositions: []opencdc.Position{want}})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Run_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cerrors.New("test-error")

	// Function Source.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cplugin.SourceRunStream) error {
			defer close(closeCh)
			serverStream := stream.Server()
			_, _ = serverStream.Recv() // receive ack and fail
			return want
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	err = clientStream.Send(cplugin.SourceRunRequest{AckPositions: []opencdc.Position{opencdc.Position("test-position")}})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Ack")
	}

	// Error is returned through the Read function, that's the incoming stream.
	_, err = clientStream.Recv()
	// Unwrap inner-most error
	var got error
	for unwrapped := err; unwrapped != nil; {
		got = unwrapped
		unwrapped = cerrors.Unwrap(unwrapped)
	}

	is.Equal(got.Error(), want.Error())

	// Ack returns just a generic error
	err = clientStream.Send(cplugin.SourceRunRequest{AckPositions: []opencdc.Position{opencdc.Position("test-position")}})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	mockSource.EXPECT().
		Teardown(gomock.Any(), cplugin.SourceTeardownRequest{}).
		Return(cplugin.SourceTeardownResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	teardownResp, err := source.Teardown(ctx, cplugin.SourceTeardownRequest{})
	is.NoErr(err)
	is.Equal(teardownResp, cplugin.SourceTeardownResponse{})
}

func testSource_Teardown_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cerrors.New("init error")
	mockSource.EXPECT().
		Teardown(gomock.Any(), cplugin.SourceTeardownRequest{}).
		Return(cplugin.SourceTeardownResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, got := source.Teardown(ctx, cplugin.SourceTeardownRequest{})
	is.Equal(got.Error(), want.Error())
}

func testSource_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cplugin.SourceLifecycleOnCreatedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockSource.EXPECT().
		LifecycleOnCreated(gomock.Any(), want).
		Return(cplugin.SourceLifecycleOnCreatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnCreated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, cplugin.SourceLifecycleOnCreatedResponse{})
}

func testSource_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cplugin.SourceLifecycleOnUpdatedRequest{
		ConfigBefore: map[string]string{"foo": "bar"},
		ConfigAfter:  map[string]string{"foo": "baz"},
	}

	mockSource.EXPECT().
		LifecycleOnUpdated(gomock.Any(), want).
		Return(cplugin.SourceLifecycleOnUpdatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnUpdated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, cplugin.SourceLifecycleOnUpdatedResponse{})
}

func testSource_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cplugin.SourceLifecycleOnDeletedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockSource.EXPECT().
		LifecycleOnDeleted(gomock.Any(), want).
		Return(cplugin.SourceLifecycleOnDeletedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnDeleted(ctx, want)
	is.NoErr(err)
	is.Equal(resp, cplugin.SourceLifecycleOnDeletedResponse{})
}

func testSource_BlockingFunctions(t *testing.T, tdf testDispenserFunc) {
	testCases := []struct {
		name               string
		prepareExpectation func(m *mock.MockSourcePlugin, blockUntil chan struct{})
		callFn             func(context.Context, SourcePlugin) error
	}{{
		name: "Configure",
		prepareExpectation: func(m *mock.MockSourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Configure(gomock.Any(), cplugin.SourceConfigureRequest{}).
				Do(func(context.Context, cplugin.SourceConfigureRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Configure(ctx, cplugin.SourceConfigureRequest{})
			return err
		},
	}, {
		name: "Open",
		prepareExpectation: func(m *mock.MockSourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Open(gomock.Any(), cplugin.SourceOpenRequest{}).
				Do(func(context.Context, cplugin.SourceOpenRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Open(ctx, cplugin.SourceOpenRequest{})
			return err
		},
	}, {
		name: "Stop",
		prepareExpectation: func(m *mock.MockSourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Stop(gomock.Any(), cplugin.SourceStopRequest{}).
				Do(func(context.Context, cplugin.SourceStopRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Stop(ctx, cplugin.SourceStopRequest{})
			return err
		},
	}, {
		name: "Teardown",
		prepareExpectation: func(m *mock.MockSourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Teardown(gomock.Any(), cplugin.SourceTeardownRequest{}).
				Do(func(context.Context, cplugin.SourceTeardownRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Teardown(ctx, cplugin.SourceTeardownRequest{})
			return err
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			dispenser, _, mockSource, _ := tdf(t)

			blockUntil := make(chan struct{})
			tc.prepareExpectation(mockSource, blockUntil)

			source, err := dispenser.DispenseSource()
			is.NoErr(err)

			fnErr := make(chan error)
			go func() {
				// call function in goroutine, because the mock will block
				fnErr <- tc.callFn(ctx, source)
			}()

			// ensure that the call to the function is blocked
			select {
			case <-fnErr:
				t.Fatal("plugin call should block")
			case <-time.After(time.Second):
			}

			// cancelling the context should unblock the call, regardless if the
			// mock is still blocking
			cancel()
			select {
			case err = <-fnErr:
				is.Equal(err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("call to plugin should have stopped blocking")
			}

			// release the blocked call to the mock
			close(blockUntil)
		})
	}
}

// -----------------
// -- DESTINATION --
// -----------------

// func testDestination_Configure_Success(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	cfg := map[string]string{
// 		"foo":   "bar",
// 		"empty": "",
// 	}
// 	want := cerrors.New("init error")
// 	mockDestination.EXPECT().
// 		Configure(gomock.Any(), cplugin.DestinationConfigureRequest{Config: cfg}).
// 		Return(cplugin.DestinationConfigureResponse{}, want)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	got := destination.Configure(ctx, cfg)
// 	is.Equal(got.Error(), want.Error())
// }
//
// func testDestination_Configure_Fail(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	mockDestination.EXPECT().
// 		Configure(gomock.Any(), cplugin.DestinationConfigureRequest{Config: nil}).
// 		Return(cplugin.DestinationConfigureResponse{}, nil)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Configure(ctx, map[string]string{})
// 	is.NoErr(err)
// }
//
// func testDestination_Open_Success(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	openResp, err := destination.Open(ctx, cplugin.DestinationOpenRequest{})
// 	is.NoErr(err)
// 	is.Equal(openResp, cplugin.DestinationOpenResponse{})
// }
//
// func testDestination_Open_Fail(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := cerrors.New("test error")
//
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, want)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	got := destination.Open(ctx)
// 	is.Equal(got.Error(), want.Error())
// }
//
// func testDestination_Write_Success(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := opencdc.Record{
// 		Position:  []byte("test-position"),
// 		Operation: opencdc.OperationUpdate,
// 		Metadata:  map[string]string{"foo": "bar"},
// 		Key:       opencdc.RawData("raw-key"),
// 		Payload: cplugin.Change{
// 			Before: cplugin.StructuredData{"baz": "qux1"},
// 			After:  cplugin.StructuredData{"baz": "qux2"},
// 		},
// 	}
//
// 	// Function Destination.Run is called in a goroutine, we have to wait for it to
// 	// run to prove this works.
// 	closeCh := make(chan struct{})
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Run(gomock.Any(), gomock.Any()).
// 		DoAndReturn(func(_ context.Context, stream cplugin.DestinationRunStream) error {
// 			defer close(closeCh)
// 			got, err := stream.Recv()
// 			is.NoErr(err)
// 			if diff := cmp.Diff(got.Record, want); diff != "" {
// 				t.Errorf("expected ack: %s", diff)
// 			}
// 			return nil
// 		})
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Open(ctx)
// 	is.NoErr(err)
//
// 	err = destination.Write(ctx, opencdc.Record{
// 		Position:  want.Position,
// 		Operation: want.Operation,
// 		Metadata:  want.Metadata,
// 		Key:       opencdc.RawData{Raw: want.Key.(opencdc.RawData)},
// 		Payload: opencdc.Change{
// 			Before: opencdc.StructuredData(want.Payload.Before.(cplugin.StructuredData)),
// 			After:  opencdc.StructuredData(want.Payload.After.(cplugin.StructuredData)),
// 		},
// 	})
// 	is.NoErr(err)
//
// 	select {
// 	case <-closeCh:
// 	case <-time.After(time.Second):
// 		t.Fatal("should've received call to destination.Write")
// 	}
//
// 	// wait for stream closing to propagate from plugin to Conduit
// 	time.Sleep(time.Millisecond * 50)
//
// 	err = destination.Write(ctx, opencdc.Record{})
// 	is.True(cerrors.Is(err, ErrStreamNotOpen))
// }
//
// func testDestination_Write_WithoutRun(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, _ := tdf(t)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Write(ctx, opencdc.Record{})
// 	is.True(cerrors.Is(err, ErrStreamNotOpen))
// }
//
// func testDestination_Ack_Success(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	var want []opencdc.Position
// 	for i := 0; i < 10; i++ {
// 		want = append(want, []byte(fmt.Sprintf("position-%d", i)))
// 	}
//
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Run(gomock.Any(), gomock.Any()).
// 		DoAndReturn(func(_ context.Context, stream cplugin.DestinationRunStream) error {
// 			for _, p := range want {
// 				err := stream.Send(cplugin.DestinationRunResponse{
// 					AckPosition: p,
// 				})
// 				is.NoErr(err)
// 			}
// 			return nil
// 		})
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Open(ctx)
// 	is.NoErr(err)
//
// 	var got []opencdc.Position
// 	for i := 0; i < len(want); i++ {
// 		pos, err := destination.Ack(ctx)
// 		is.NoErr(err)
// 		got = append(got, pos)
// 	}
//
// 	if diff := cmp.Diff(got, want); diff != "" {
// 		t.Errorf("expected position: %s", diff)
// 	}
// }
//
// func testDestination_Ack_WithError(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	wantPos := opencdc.Position("test-position")
// 	wantErr := cerrors.New("test error")
//
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Run(gomock.Any(), gomock.Any()).
// 		DoAndReturn(func(_ context.Context, stream cplugin.DestinationRunStream) error {
// 			err := stream.Send(cplugin.DestinationRunResponse{
// 				AckPosition: wantPos,
// 				Error:       wantErr.Error(),
// 			})
// 			is.NoErr(err)
// 			return nil
// 		})
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Open(ctx)
// 	is.NoErr(err)
//
// 	gotPos, gotErr := destination.Ack(ctx)
// 	if diff := cmp.Diff(gotPos, wantPos); diff != "" {
// 		t.Errorf("expected position: %s", diff)
// 	}
// 	is.Equal(gotErr.Error(), wantErr.Error())
// }
//
// func testDestination_Ack_WithoutRun(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, _ := tdf(t)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	_, err = destination.Ack(ctx)
// 	is.True(cerrors.Is(err, ErrStreamNotOpen))
// }
//
// func testDestination_Run_Fail(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := cerrors.New("test-error")
//
// 	// Function Destination.Run is called in a goroutine, we have to wait for it to
// 	// run to prove this works.
// 	closeCh := make(chan struct{})
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Run(gomock.Any(), gomock.Any()).
// 		DoAndReturn(func(_ context.Context, stream cplugin.DestinationRunStream) error {
// 			defer close(closeCh)
// 			_, _ = stream.Recv() // receive record and fail
// 			return want
// 		})
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Open(ctx)
// 	is.NoErr(err)
//
// 	err = destination.Write(ctx, opencdc.Record{})
// 	is.NoErr(err)
//
// 	select {
// 	case <-closeCh:
// 	case <-time.After(time.Second):
// 		t.Fatal("should've received call to destination.Write")
// 	}
//
// 	// Error is returned through the Ack function, that's the incoming stream.
// 	_, err = destination.Ack(ctx)
// 	// Unwrap inner-most error
// 	var got error
// 	for unwrapped := err; unwrapped != nil; {
// 		got = unwrapped
// 		unwrapped = cerrors.Unwrap(unwrapped)
// 	}
// 	is.Equal(got.Error(), want.Error())
//
// 	// Write returns just a generic error
// 	err = destination.Write(ctx, opencdc.Record{})
// 	is.True(cerrors.Is(err, ErrStreamNotOpen))
// }
//
// func testDestination_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := cerrors.New("init error")
// 	closeCh := make(chan struct{})
// 	stopRunCh := make(chan struct{})
// 	mockDestination.EXPECT().
// 		Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 		Return(cplugin.DestinationOpenResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Stop(gomock.Any(), cplugin.DestinationStopRequest{}).
// 		Return(cplugin.DestinationStopResponse{}, nil)
// 	mockDestination.EXPECT().
// 		Run(gomock.Any(), gomock.Any()).
// 		DoAndReturn(func(ctx context.Context, stream cplugin.DestinationRunStream) error {
// 			defer close(closeCh)
// 			<-stopRunCh
// 			return nil
// 		})
// 	mockDestination.EXPECT().
// 		Teardown(gomock.Any(), cplugin.DestinationTeardownRequest{}).
// 		Return(cplugin.DestinationTeardownResponse{}, want)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.Open(ctx)
// 	is.NoErr(err)
// 	err = destination.Stop(ctx, nil)
// 	is.NoErr(err)
//
// 	got := destination.Teardown(ctx)
// 	is.Equal(got.Error(), want.Error())
//
// 	close(stopRunCh)
// 	select {
// 	case <-closeCh:
// 	case <-time.After(time.Second):
// 		t.Fatal("should've received call to destination.Run")
// 	}
// }
//
// func testDestination_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := map[string]string{"foo": "bar"}
//
// 	mockDestination.EXPECT().
// 		LifecycleOnCreated(gomock.Any(), cplugin.DestinationLifecycleOnCreatedRequest{
// 			Config: want,
// 		}).
// 		Return(cplugin.DestinationLifecycleOnCreatedResponse{}, nil)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.LifecycleOnCreated(ctx, want)
// 	is.NoErr(err)
// }
//
// func testDestination_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	wantBefore := map[string]string{"foo": "bar"}
// 	wantAfter := map[string]string{"foo": "baz"}
//
// 	mockDestination.EXPECT().
// 		LifecycleOnUpdated(gomock.Any(), cplugin.DestinationLifecycleOnUpdatedRequest{
// 			ConfigBefore: wantBefore,
// 			ConfigAfter:  wantAfter,
// 		}).
// 		Return(cplugin.DestinationLifecycleOnUpdatedResponse{}, nil)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.LifecycleOnUpdated(ctx, wantBefore, wantAfter)
// 	is.NoErr(err)
// }
//
// func testDestination_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
// 	is := is.New(t)
// 	ctx := context.Background()
// 	dispenser, _, _, mockDestination := tdf(t)
//
// 	want := map[string]string{"foo": "bar"}
//
// 	mockDestination.EXPECT().
// 		LifecycleOnDeleted(gomock.Any(), cplugin.DestinationLifecycleOnDeletedRequest{
// 			Config: want,
// 		}).
// 		Return(cplugin.DestinationLifecycleOnDeletedResponse{}, nil)
//
// 	destination, err := dispenser.DispenseDestination()
// 	is.NoErr(err)
//
// 	err = destination.LifecycleOnDeleted(ctx, want)
// 	is.NoErr(err)
// }
//
// func testDestination_BlockingFunctions(t *testing.T, tdf testDispenserFunc) {
// 	testCases := []struct {
// 		name               string
// 		prepareExpectation func(m *mock.MockDestinationPlugin, blockUntil chan struct{})
// 		callFn             func(context.Context, DestinationPlugin) error
// 	}{{
// 		name: "Configure",
// 		prepareExpectation: func(m *mock.MockDestinationPlugin, blockUntil chan struct{}) {
// 			m.EXPECT().
// 				Configure(gomock.Any(), cplugin.DestinationConfigureRequest{}).
// 				Do(func(context.Context, cplugin.DestinationConfigureRequest) {
// 					<-blockUntil
// 				})
// 		},
// 		callFn: func(ctx context.Context, d DestinationPlugin) error {
// 			return d.Configure(ctx, map[string]string{})
// 		},
// 	}, {
// 		name: "Open",
// 		prepareExpectation: func(m *mock.MockDestinationPlugin, blockUntil chan struct{}) {
// 			m.EXPECT().
// 				Open(gomock.Any(), cplugin.DestinationOpenRequest{}).
// 				Do(func(context.Context, cplugin.DestinationOpenRequest) {
// 					<-blockUntil
// 				})
// 		},
// 		callFn: func(ctx context.Context, d DestinationPlugin) error {
// 			return d.Open(ctx)
// 		},
// 	}, {
// 		name: "Stop",
// 		prepareExpectation: func(m *mock.MockDestinationPlugin, blockUntil chan struct{}) {
// 			m.EXPECT().
// 				Stop(gomock.Any(), cplugin.DestinationStopRequest{}).
// 				Do(func(context.Context, cplugin.DestinationStopRequest) {
// 					<-blockUntil
// 				})
// 		},
// 		callFn: func(ctx context.Context, d DestinationPlugin) error {
// 			return d.Stop(ctx, nil)
// 		},
// 	}, {
// 		name: "Teardown",
// 		prepareExpectation: func(m *mock.MockDestinationPlugin, blockUntil chan struct{}) {
// 			m.EXPECT().
// 				Teardown(gomock.Any(), cplugin.DestinationTeardownRequest{}).
// 				Do(func(context.Context, cplugin.DestinationTeardownRequest) {
// 					<-blockUntil
// 				})
// 		},
// 		callFn: func(ctx context.Context, d DestinationPlugin) error {
// 			return d.Teardown(ctx)
// 		},
// 	}}
//
// 	for _, tc := range testCases {
// 		t.Run(tc.name, func(t *testing.T) {
// 			is := is.New(t)
// 			ctx, cancel := context.WithCancel(context.Background())
// 			defer cancel()
//
// 			dispenser, _, _, mockDestination := tdf(t)
//
// 			blockUntil := make(chan struct{})
// 			tc.prepareExpectation(mockDestination, blockUntil)
//
// 			destination, err := dispenser.DispenseDestination()
// 			is.NoErr(err)
//
// 			fnErr := make(chan error)
// 			go func() {
// 				// call function in goroutine, because the mock will block
// 				fnErr <- tc.callFn(ctx, destination)
// 			}()
//
// 			// ensure that the call to the function is blocked
// 			select {
// 			case <-fnErr:
// 				t.Fatal("plugin call should block")
// 			case <-time.After(time.Second):
// 			}
//
// 			// cancelling the context should unblock the call, regardless if the
// 			// mock is still blocking
// 			cancel()
// 			select {
// 			case err = <-fnErr:
// 				is.Equal(err, context.Canceled)
// 			case <-time.After(time.Second):
// 				t.Fatal("call to plugin should have stopped blocking")
// 			}
//
// 			// release the blocked call to the mock
// 			close(blockUntil)
// 		})
// 	}
// }
