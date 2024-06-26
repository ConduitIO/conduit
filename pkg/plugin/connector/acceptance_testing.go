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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// AcceptanceTest is the acceptance test that all implementations of
// plugins should pass. It should manually be called from a test case in each
// implementation:
//
//	func TestPlugin(t *testing.T) {
//	    testDispenser := func() {...}
//	    plugin.AcceptanceTest(t, testDispenser)
//	}
func AcceptanceTest(t *testing.T, tdf testDispenserFunc) {
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
	run(t, tdf, testSource_Stream_WithoutRunPanics)
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
	run(t, tdf, testDestination_Configure_Success)
	run(t, tdf, testDestination_Configure_Fail)
	run(t, tdf, testDestination_Open_Success)
	run(t, tdf, testDestination_Open_Fail)
	run(t, tdf, testDestination_Run_Success)
	run(t, tdf, testDestination_Run_Fail)
	run(t, tdf, testDestination_Stream_WithoutRunPanics)
	run(t, tdf, testDestination_StreamRecv_Success)
	run(t, tdf, testDestination_StreamRecv_WithError)
	run(t, tdf, testDestination_Teardown_Success)
	run(t, tdf, testDestination_Teardown_Fail)
	run(t, tdf, testDestination_Lifecycle_OnCreated)
	run(t, tdf, testDestination_Lifecycle_OnUpdated)
	run(t, tdf, testDestination_Lifecycle_OnDeleted)
	run(t, tdf, testDestination_BlockingFunctions)
}

func run(t *testing.T, tdf testDispenserFunc, test func(*testing.T, testDispenserFunc)) {
	name := runtime.FuncForPC(reflect.ValueOf(test).Pointer()).Name()
	name = name[strings.LastIndex(name, ".")+1:]
	t.Run(name, func(t *testing.T) { test(t, tdf) })
}

type testDispenserFunc func(*testing.T) (Dispenser, *mock.SpecifierPlugin, *mock.SourcePlugin, *mock.DestinationPlugin)

// ---------------
// -- SPECIFIER --
// ---------------

func testSpecifier_Specify_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := pconnector.SpecifierSpecifyResponse{
		Specification: pconnector.Specification{
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
		Specify(gomock.Any(), pconnector.SpecifierSpecifyRequest{}).
		Return(want, nil)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	got, err := specifier.Specify(ctx, pconnector.SpecifierSpecifyRequest{})
	is.NoErr(err)

	is.Equal("", cmp.Diff(want, got))
}

func testSpecifier_Specify_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := cerrors.New("specify error")
	mockSpecifier.EXPECT().
		Specify(gomock.Any(), pconnector.SpecifierSpecifyRequest{}).
		Return(pconnector.SpecifierSpecifyResponse{}, want)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	_, got := specifier.Specify(ctx, pconnector.SpecifierSpecifyRequest{})
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
		Configure(gomock.Any(), pconnector.SourceConfigureRequest{Config: nil}).
		Return(pconnector.SourceConfigureResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	got, err := source.Configure(ctx, pconnector.SourceConfigureRequest{Config: nil})
	is.NoErr(err)
	is.Equal(got, pconnector.SourceConfigureResponse{})
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
		Configure(gomock.Any(), pconnector.SourceConfigureRequest{Config: cfg}).
		Return(pconnector.SourceConfigureResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, got := source.Configure(ctx, pconnector.SourceConfigureRequest{Config: cfg})
	is.Equal(got.Error(), want.Error())
}

func testSource_Open_WithPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	pos := opencdc.Position("test-position")

	mockSource.EXPECT().
		Open(gomock.Any(), pconnector.SourceOpenRequest{Position: pos}).
		Return(pconnector.SourceOpenResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, err = source.Open(ctx, pconnector.SourceOpenRequest{Position: pos})
	is.NoErr(err)
}

func testSource_Open_EmptyPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	mockSource.EXPECT().
		Open(gomock.Any(), pconnector.SourceOpenRequest{Position: nil}).
		Return(pconnector.SourceOpenResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.Open(ctx, pconnector.SourceOpenRequest{Position: nil})
	is.NoErr(err)
	is.Equal(resp, pconnector.SourceOpenResponse{})
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
		DoAndReturn(func(ctx context.Context, stream pconnector.SourceRunStream) error {
			serverStream := stream.Server()
			for _, r := range want {
				err := serverStream.Send(pconnector.SourceRunResponse{Records: []opencdc.Record{r}})
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

func testSource_Stream_WithoutRunPanics(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	dispenser, _, _, _ := tdf(t)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()

	defer func() {
		is.True(recover() != nil)
	}()
	stream.Client()
	t.Fail() // getting a stream without calling Run should panic
}

func testSource_StreamRecv_AfterStop(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := pconnector.SourceStopResponse{
		LastPosition: []byte("foo"),
	}

	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, pconnector.SourceRunStream) error {
			<-stopRunCh
			return nil
		})
	mockSource.EXPECT().
		Stop(gomock.Any(), pconnector.SourceStopRequest{}).
		DoAndReturn(func(context.Context, pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
			close(stopRunCh)
			return want, nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	got, err := source.Stop(ctx, pconnector.SourceStopRequest{})
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
		DoAndReturn(func(ctx context.Context, stream pconnector.SourceRunStream) error {
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
		DoAndReturn(func(_ context.Context, stream pconnector.SourceRunStream) error {
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

	err = clientStream.Send(pconnector.SourceRunRequest{AckPositions: []opencdc.Position{want}})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Ack")
	}

	// wait for stream closing to propagate from plugin to Conduit
	time.Sleep(time.Millisecond * 50)

	// acking after the stream is closed should result in an error
	err = clientStream.Send(pconnector.SourceRunRequest{AckPositions: []opencdc.Position{want}})
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
		DoAndReturn(func(_ context.Context, stream pconnector.SourceRunStream) error {
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

	err = clientStream.Send(pconnector.SourceRunRequest{AckPositions: []opencdc.Position{opencdc.Position("test-position")}})
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
	err = clientStream.Send(pconnector.SourceRunRequest{AckPositions: []opencdc.Position{opencdc.Position("test-position")}})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	mockSource.EXPECT().
		Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	teardownResp, err := source.Teardown(ctx, pconnector.SourceTeardownRequest{})
	is.NoErr(err)
	is.Equal(teardownResp, pconnector.SourceTeardownResponse{})
}

func testSource_Teardown_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cerrors.New("init error")
	mockSource.EXPECT().
		Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, got := source.Teardown(ctx, pconnector.SourceTeardownRequest{})
	is.Equal(got.Error(), want.Error())
}

func testSource_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := pconnector.SourceLifecycleOnCreatedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockSource.EXPECT().
		LifecycleOnCreated(gomock.Any(), want).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnCreated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.SourceLifecycleOnCreatedResponse{})
}

func testSource_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := pconnector.SourceLifecycleOnUpdatedRequest{
		ConfigBefore: map[string]string{"foo": "bar"},
		ConfigAfter:  map[string]string{"foo": "baz"},
	}

	mockSource.EXPECT().
		LifecycleOnUpdated(gomock.Any(), want).
		Return(pconnector.SourceLifecycleOnUpdatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnUpdated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.SourceLifecycleOnUpdatedResponse{})
}

func testSource_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := pconnector.SourceLifecycleOnDeletedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockSource.EXPECT().
		LifecycleOnDeleted(gomock.Any(), want).
		Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	resp, err := source.LifecycleOnDeleted(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.SourceLifecycleOnDeletedResponse{})
}

func testSource_BlockingFunctions(t *testing.T, tdf testDispenserFunc) {
	testCases := []struct {
		name               string
		prepareExpectation func(m *mock.SourcePlugin, blockUntil chan struct{})
		callFn             func(context.Context, SourcePlugin) error
	}{{
		name: "Configure",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Configure(gomock.Any(), pconnector.SourceConfigureRequest{}).
				Do(func(context.Context, pconnector.SourceConfigureRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Configure(ctx, pconnector.SourceConfigureRequest{})
			return err
		},
	}, {
		name: "Open",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Open(gomock.Any(), pconnector.SourceOpenRequest{}).
				Do(func(context.Context, pconnector.SourceOpenRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Open(ctx, pconnector.SourceOpenRequest{})
			return err
		},
	}, {
		name: "Stop",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Stop(gomock.Any(), pconnector.SourceStopRequest{}).
				Do(func(context.Context, pconnector.SourceStopRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Stop(ctx, pconnector.SourceStopRequest{})
			return err
		},
	}, {
		name: "Teardown",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
				Do(func(context.Context, pconnector.SourceTeardownRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Teardown(ctx, pconnector.SourceTeardownRequest{})
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

func testDestination_Configure_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	mockDestination.EXPECT().
		Configure(gomock.Any(), pconnector.DestinationConfigureRequest{Config: nil}).
		Return(pconnector.DestinationConfigureResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	resp, err := destination.Configure(ctx, pconnector.DestinationConfigureRequest{Config: nil})
	is.NoErr(err)
	is.Equal(resp, pconnector.DestinationConfigureResponse{})
}

func testDestination_Configure_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	cfg := map[string]string{
		"foo":   "bar",
		"empty": "",
	}
	want := cerrors.New("init error")
	mockDestination.EXPECT().
		Configure(gomock.Any(), pconnector.DestinationConfigureRequest{Config: cfg}).
		Return(pconnector.DestinationConfigureResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	_, got := destination.Configure(ctx, pconnector.DestinationConfigureRequest{Config: cfg})
	is.Equal(got.Error(), want.Error())
}

func testDestination_Open_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	mockDestination.EXPECT().
		Open(gomock.Any(), pconnector.DestinationOpenRequest{}).
		Return(pconnector.DestinationOpenResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	openResp, err := destination.Open(ctx, pconnector.DestinationOpenRequest{})
	is.NoErr(err)
	is.Equal(openResp, pconnector.DestinationOpenResponse{})
}

func testDestination_Open_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := cerrors.New("test error")

	mockDestination.EXPECT().
		Open(gomock.Any(), pconnector.DestinationOpenRequest{}).
		Return(pconnector.DestinationOpenResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	_, got := destination.Open(ctx, pconnector.DestinationOpenRequest{})
	is.Equal(got.Error(), want.Error())
}

func testDestination_Run_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := opencdc.Record{
		Position:  []byte("test-position"),
		Operation: opencdc.OperationUpdate,
		Metadata:  map[string]string{"foo": "bar"},
		Key:       opencdc.RawData("raw-key"),
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{"baz": "qux1"},
			After:  opencdc.StructuredData{"baz": "qux2"},
		},
	}

	// Function Destination.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.DestinationRunStream) error {
			defer close(closeCh)
			serverStream := stream.Server()
			got, err := serverStream.Recv()
			is.NoErr(err)
			is.Equal(len(got.Records), 1)
			is.Equal("", cmp.Diff(got.Records[0], want, cmpopts.IgnoreUnexported(opencdc.Record{})))
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	stream := destination.NewStream()
	err = destination.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	err = clientStream.Send(pconnector.DestinationRunRequest{
		Records: []opencdc.Record{want},
	})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Write")
	}

	// wait for stream closing to propagate from plugin to Conduit
	time.Sleep(time.Millisecond * 100)

	err = clientStream.Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{{}}})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testDestination_Run_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := cerrors.New("test-error")

	// Function Destination.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.DestinationRunStream) error {
			defer close(closeCh)
			serverStream := stream.Server()
			_, _ = serverStream.Recv() // receive record and fail
			return want
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	stream := destination.NewStream()
	err = destination.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	err = clientStream.Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{{}}})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Write")
	}

	// Error is returned through the Recv function, that's the incoming stream.
	_, err = clientStream.Recv()
	// Unwrap inner-most error
	var got error
	for unwrapped := err; unwrapped != nil; {
		got = unwrapped
		unwrapped = cerrors.Unwrap(unwrapped)
	}
	is.Equal(got.Error(), want.Error())

	// Send returns just a generic error
	err = clientStream.Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{{}}})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testDestination_Stream_WithoutRunPanics(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	dispenser, _, _, _ := tdf(t)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	stream := destination.NewStream()

	defer func() {
		is.True(recover() != nil)
	}()
	stream.Client()
	t.Fail() // getting a stream without calling Run should panic
}

func testDestination_StreamRecv_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	var want []opencdc.Position
	for i := 0; i < 10; i++ {
		want = append(want, []byte(fmt.Sprintf("position-%d", i)))
	}

	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.DestinationRunStream) error {
			serverStream := stream.Server()
			for _, p := range want {
				err := serverStream.Send(pconnector.DestinationRunResponse{
					Acks: []pconnector.DestinationRunResponseAck{{
						Position: p,
					}},
				})
				is.NoErr(err)
			}
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	stream := destination.NewStream()
	err = destination.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	var got []opencdc.Position
	for i := 0; i < len(want); i++ {
		resp, err := clientStream.Recv()
		is.NoErr(err)
		for _, ack := range resp.Acks {
			is.Equal("", ack.Error)
			got = append(got, ack.Position)
		}
	}

	is.Equal("", cmp.Diff(want, got))
}

func testDestination_StreamRecv_WithError(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := pconnector.DestinationRunResponseAck{
		Position: opencdc.Position("test-position"),
		Error:    "test error",
	}

	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.DestinationRunStream) error {
			serverStream := stream.Server()
			err := serverStream.Send(pconnector.DestinationRunResponse{
				Acks: []pconnector.DestinationRunResponseAck{want},
			})
			is.NoErr(err)
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	stream := destination.NewStream()
	err = destination.Run(ctx, stream)
	is.NoErr(err)

	clientStream := stream.Client()

	got, err := clientStream.Recv()
	is.NoErr(err)
	is.Equal("", cmp.Diff(got.Acks, []pconnector.DestinationRunResponseAck{want}))
}

func testDestination_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	mockDestination.EXPECT().
		Teardown(gomock.Any(), pconnector.DestinationTeardownRequest{}).
		Return(pconnector.DestinationTeardownResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	teardownResp, err := destination.Teardown(ctx, pconnector.DestinationTeardownRequest{})
	is.NoErr(err)
	is.Equal(teardownResp, pconnector.DestinationTeardownResponse{})
}

func testDestination_Teardown_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := cerrors.New("init error")
	mockDestination.EXPECT().
		Teardown(gomock.Any(), pconnector.DestinationTeardownRequest{}).
		Return(pconnector.DestinationTeardownResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	_, got := destination.Teardown(ctx, pconnector.DestinationTeardownRequest{})
	is.Equal(got.Error(), want.Error())
}

func testDestination_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := pconnector.DestinationLifecycleOnCreatedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockDestination.EXPECT().
		LifecycleOnCreated(gomock.Any(), want).
		Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	resp, err := destination.LifecycleOnCreated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.DestinationLifecycleOnCreatedResponse{})
}

func testDestination_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := pconnector.DestinationLifecycleOnUpdatedRequest{
		ConfigBefore: map[string]string{"foo": "bar"},
		ConfigAfter:  map[string]string{"foo": "baz"},
	}

	mockDestination.EXPECT().
		LifecycleOnUpdated(gomock.Any(), want).
		Return(pconnector.DestinationLifecycleOnUpdatedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	resp, err := destination.LifecycleOnUpdated(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.DestinationLifecycleOnUpdatedResponse{})
}

func testDestination_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := pconnector.DestinationLifecycleOnDeletedRequest{
		Config: map[string]string{"foo": "bar"},
	}

	mockDestination.EXPECT().
		LifecycleOnDeleted(gomock.Any(), want).
		Return(pconnector.DestinationLifecycleOnDeletedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	resp, err := destination.LifecycleOnDeleted(ctx, want)
	is.NoErr(err)
	is.Equal(resp, pconnector.DestinationLifecycleOnDeletedResponse{})
}

func testDestination_BlockingFunctions(t *testing.T, tdf testDispenserFunc) {
	testCases := []struct {
		name               string
		prepareExpectation func(m *mock.DestinationPlugin, blockUntil chan struct{})
		callFn             func(context.Context, DestinationPlugin) error
	}{{
		name: "Configure",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Configure(gomock.Any(), pconnector.DestinationConfigureRequest{}).
				Do(func(context.Context, pconnector.DestinationConfigureRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			_, err := d.Configure(ctx, pconnector.DestinationConfigureRequest{})
			return err
		},
	}, {
		name: "Open",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Open(gomock.Any(), pconnector.DestinationOpenRequest{}).
				Do(func(context.Context, pconnector.DestinationOpenRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			_, err := d.Open(ctx, pconnector.DestinationOpenRequest{})
			return err
		},
	}, {
		name: "Stop",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Stop(gomock.Any(), pconnector.DestinationStopRequest{}).
				Do(func(context.Context, pconnector.DestinationStopRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			_, err := d.Stop(ctx, pconnector.DestinationStopRequest{})
			return err
		},
	}, {
		name: "Teardown",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Teardown(gomock.Any(), pconnector.DestinationTeardownRequest{}).
				Do(func(context.Context, pconnector.DestinationTeardownRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			_, err := d.Teardown(ctx, pconnector.DestinationTeardownRequest{})
			return err
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			dispenser, _, _, mockDestination := tdf(t)

			blockUntil := make(chan struct{})
			tc.prepareExpectation(mockDestination, blockUntil)

			destination, err := dispenser.DispenseDestination()
			is.NoErr(err)

			fnErr := make(chan error)
			go func() {
				// call function in goroutine, because the mock will block
				fnErr <- tc.callFn(ctx, destination)
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
