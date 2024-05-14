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
	"github.com/conduitio/conduit-connector-protocol/cpluginv2"
	"github.com/conduitio/conduit-connector-protocol/cpluginv2/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// AcceptanceTestV2 is the acceptance test that all implementations of v1
// plugins should pass. It should manually be called from a test case in each
// implementation:
//
//	func TestPlugin(t *testing.T) {
//	    testDispenser := func() {...}
//	    plugin.AcceptanceTestV2(t, testDispenser)
//	}
func AcceptanceTestV2(t *testing.T, tdf testDispenserFunc) {
	// specifier tests
	run(t, tdf, testSpecifier_Specify_Success)
	run(t, tdf, testSpecifier_Specify_Fail)

	// source tests
	run(t, tdf, testSource_Configure_Success)
	run(t, tdf, testSource_Configure_Fail)
	run(t, tdf, testSource_Start_WithPosition)
	run(t, tdf, testSource_Start_EmptyPosition)
	run(t, tdf, testSource_Read_Success)
	run(t, tdf, testSource_Read_WithoutStart)
	run(t, tdf, testSource_Read_AfterStop)
	run(t, tdf, testSource_Read_CancelContext)
	run(t, tdf, testSource_Ack_Success)
	run(t, tdf, testSource_Ack_WithoutStart)
	run(t, tdf, testSource_Run_Fail)
	run(t, tdf, testSource_Teardown_Success)
	run(t, tdf, testSource_Lifecycle_OnCreated)
	run(t, tdf, testSource_Lifecycle_OnUpdated)
	run(t, tdf, testSource_Lifecycle_OnDeleted)
	run(t, tdf, testSource_BlockingFunctions)

	// destination tests
	run(t, tdf, testDestination_Configure_Success)
	run(t, tdf, testDestination_Configure_Fail)
	run(t, tdf, testDestination_Start_Success)
	run(t, tdf, testDestination_Start_Fail)
	run(t, tdf, testDestination_Write_Success)
	run(t, tdf, testDestination_Write_WithoutStart)
	run(t, tdf, testDestination_Ack_Success)
	run(t, tdf, testDestination_Ack_WithError)
	run(t, tdf, testDestination_Ack_WithoutStart)
	run(t, tdf, testDestination_Run_Fail)
	run(t, tdf, testDestination_Teardown_Success)
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
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := Specification{
		Name:        "test-name",
		Summary:     "A short summary",
		Description: "A long description",
		Version:     "v1.2.3",
		Author:      "Donald Duck",
		SourceParams: config.Parameters{
			"param1.1": {Default: "foo", Type: config.ParameterTypeString, Description: "Param 1.1 description", Validations: []config.Validation{}},
			"param1.2": {Default: "bar", Type: config.ParameterTypeString, Description: "Param 1.2 description", Validations: []config.Validation{config.ValidationRequired{}}},
		},
		DestinationParams: config.Parameters{
			"param2.1": {Default: "baz", Type: config.ParameterTypeString, Description: "Param 2.1 description", Validations: []config.Validation{}},
			"param2.2": {Default: "qux", Type: config.ParameterTypeString, Description: "Param 2.2 description", Validations: []config.Validation{config.ValidationRequired{}}},
		},
	}

	mockSpecifier.EXPECT().
		Specify(gomock.Any(), cpluginv2.SpecifierSpecifyRequest{}).
		Return(cpluginv2.SpecifierSpecifyResponse{
			Name:              want.Name,
			Summary:           want.Summary,
			Description:       want.Description,
			Version:           want.Version,
			Author:            want.Author,
			SourceParams:      want.SourceParams,
			DestinationParams: want.DestinationParams,
		}, nil)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	got, err := specifier.Specify()
	is.NoErr(err)

	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("expected specification: %s", diff)
	}
}

func testSpecifier_Specify_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	dispenser, mockSpecifier, _, _ := tdf(t)

	want := cerrors.New("specify error")
	mockSpecifier.EXPECT().
		Specify(gomock.Any(), cpluginv2.SpecifierSpecifyRequest{}).
		Return(cpluginv2.SpecifierSpecifyResponse{}, want)

	specifier, err := dispenser.DispenseSpecifier()
	is.NoErr(err)

	_, got := specifier.Specify()
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
		Configure(gomock.Any(), cpluginv2.SourceConfigureRequest{Config: nil}).
		Return(cpluginv2.SourceConfigureResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	got := source.Configure(ctx, map[string]string{})
	is.Equal(got, nil)
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
		Configure(gomock.Any(), cpluginv2.SourceConfigureRequest{Config: cfg}).
		Return(cpluginv2.SourceConfigureResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	got := source.Configure(ctx, cfg)
	is.Equal(got.Error(), want.Error())
}

func testSource_Start_WithPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	pos := opencdc.Position("test-position")

	// Function Source.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: pos}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, cpluginv2.SourceRunStream) error {
			close(closeCh)
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, pos)
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Run")
	}
}

func testSource_Start_EmptyPosition(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	// Function Source.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, cpluginv2.SourceRunStream) error {
			close(closeCh)
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Run")
	}
}

func testSource_Read_Success(t *testing.T, tdf testDispenserFunc) {
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
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cpluginv2.SourceRunStream) error {
			for _, r := range want {
				err := stream.Send(cpluginv2.SourceRunResponse{Record: r})
				if err != nil {
					return err
				}
			}
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	var got []opencdc.Record
	for i := 0; i < len(want); i++ {
		rec, err := source.Read(ctx)
		is.NoErr(err)
		got = append(got, rec)
	}
	is.Equal(
		"",
		cmp.Diff(want, got,
			cmpopts.IgnoreUnexported(opencdc.Record{}),
		),
	)
}

func testSource_Read_WithoutStart(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, _ := tdf(t)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	_, err = source.Read(ctx)
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Read_AfterStop(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, cpluginv2.SourceRunStream) error {
			<-stopRunCh
			return nil
		})
	mockSource.EXPECT().
		Stop(gomock.Any(), cpluginv2.SourceStopRequest{}).
		DoAndReturn(func(context.Context, cpluginv2.SourceStopRequest) (cpluginv2.SourceStopResponse, error) {
			close(stopRunCh)
			return cpluginv2.SourceStopResponse{
				LastPosition: []byte("foo"),
			}, nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	gotLastPosition, err := source.Stop(ctx)
	is.NoErr(err)
	is.Equal(gotLastPosition, opencdc.Position("foo"))

	_, err = source.Read(ctx)
	is.True(cerrors.Is(err, ErrStreamNotOpen))

	select {
	case <-stopRunCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Stop")
	}
}

func testSource_Read_CancelContext(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cpluginv2.SourceRunStream) error {
			<-stopRunCh
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	startCtx, startCancel := context.WithCancel(ctx)
	err = source.Start(startCtx, nil)
	is.NoErr(err)

	// calling read when source didn't produce records should block until start
	// ctx is cancelled
	time.AfterFunc(time.Millisecond*50, func() {
		startCancel()
	})

	_, err = source.Read(ctx)
	is.True(cerrors.Is(err, context.Canceled))

	close(stopRunCh) // stop run channel
}

func testSource_Ack_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := []byte("test-position")

	// Function Source.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.SourceRunStream) error {
			defer close(closeCh)
			got, err := stream.Recv()
			is.NoErr(err)
			if diff := cmp.Diff(got.AckPosition, want); diff != "" {
				t.Errorf("expected ack: %s", diff)
			}
			return nil
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	err = source.Ack(ctx, opencdc.Position("test-position"))
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Ack")
	}

	// wait for stream closing to propagate from plugin to Conduit
	time.Sleep(time.Millisecond * 50)

	// acking after the stream is closed should result in an error
	err = source.Ack(ctx, opencdc.Position("test-position"))
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Ack_WithoutStart(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, _ := tdf(t)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Ack(ctx, []byte("test-position"))
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
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.SourceRunStream) error {
			defer close(closeCh)
			_, _ = stream.Recv() // receive ack and fail
			return want
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	err = source.Ack(ctx, opencdc.Position("test-position"))
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Ack")
	}

	// Error is returned through the Read function, that's the incoming stream.
	_, err = source.Read(ctx)
	// Unwrap inner-most error
	var got error
	for unwrapped := err; unwrapped != nil; {
		got = unwrapped
		unwrapped = cerrors.Unwrap(unwrapped)
	}

	is.Equal(got.Error(), want.Error())

	// Ack returns just a generic error
	err = source.Ack(ctx, opencdc.Position("test-position"))
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testSource_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := cerrors.New("init error")
	closeCh := make(chan struct{})
	stopRunCh := make(chan struct{})
	mockSource.EXPECT().
		Start(gomock.Any(), cpluginv2.SourceStartRequest{Position: nil}).
		Return(cpluginv2.SourceStartResponse{}, nil)
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cpluginv2.SourceRunStream) error {
			defer close(closeCh)
			<-stopRunCh
			return nil
		})
	mockSource.EXPECT().
		Teardown(gomock.Any(), cpluginv2.SourceTeardownRequest{}).
		Return(cpluginv2.SourceTeardownResponse{}, want)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.Start(ctx, nil)
	is.NoErr(err)

	got := source.Teardown(ctx)
	is.Equal(got.Error(), want.Error())

	close(stopRunCh)
	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to source.Run")
	}
}

func testSource_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := map[string]string{"foo": "bar"}

	mockSource.EXPECT().
		LifecycleOnCreated(gomock.Any(), cpluginv2.SourceLifecycleOnCreatedRequest{
			Config: want,
		}).
		Return(cpluginv2.SourceLifecycleOnCreatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.LifecycleOnCreated(ctx, want)
	is.NoErr(err)
}

func testSource_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	wantBefore := map[string]string{"foo": "bar"}
	wantAfter := map[string]string{"foo": "baz"}

	mockSource.EXPECT().
		LifecycleOnUpdated(gomock.Any(), cpluginv2.SourceLifecycleOnUpdatedRequest{
			ConfigBefore: wantBefore,
			ConfigAfter:  wantAfter,
		}).
		Return(cpluginv2.SourceLifecycleOnUpdatedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.LifecycleOnUpdated(ctx, wantBefore, wantAfter)
	is.NoErr(err)
}

func testSource_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, mockSource, _ := tdf(t)

	want := map[string]string{"foo": "bar"}

	mockSource.EXPECT().
		LifecycleOnDeleted(gomock.Any(), cpluginv2.SourceLifecycleOnDeletedRequest{
			Config: want,
		}).
		Return(cpluginv2.SourceLifecycleOnDeletedResponse{}, nil)

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	err = source.LifecycleOnDeleted(ctx, want)
	is.NoErr(err)
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
				Configure(gomock.Any(), cpluginv2.SourceConfigureRequest{}).
				Do(func(context.Context, cpluginv2.SourceConfigureRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			return d.Configure(ctx, map[string]string{})
		},
	}, {
		name: "Start",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Start(gomock.Any(), cpluginv2.SourceStartRequest{}).
				Do(func(context.Context, cpluginv2.SourceStartRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			return d.Start(ctx, nil)
		},
	}, {
		name: "Stop",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Stop(gomock.Any(), cpluginv2.SourceStopRequest{}).
				Do(func(context.Context, cpluginv2.SourceStopRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			_, err := d.Stop(ctx)
			return err
		},
	}, {
		name: "Teardown",
		prepareExpectation: func(m *mock.SourcePlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Teardown(gomock.Any(), cpluginv2.SourceTeardownRequest{}).
				Do(func(context.Context, cpluginv2.SourceTeardownRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d SourcePlugin) error {
			return d.Teardown(ctx)
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

	cfg := map[string]string{
		"foo":   "bar",
		"empty": "",
	}
	want := cerrors.New("init error")
	mockDestination.EXPECT().
		Configure(gomock.Any(), cpluginv2.DestinationConfigureRequest{Config: cfg}).
		Return(cpluginv2.DestinationConfigureResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	got := destination.Configure(ctx, cfg)
	is.Equal(got.Error(), want.Error())
}

func testDestination_Configure_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	mockDestination.EXPECT().
		Configure(gomock.Any(), cpluginv2.DestinationConfigureRequest{Config: nil}).
		Return(cpluginv2.DestinationConfigureResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Configure(ctx, map[string]string{})
	is.NoErr(err)
}

func testDestination_Start_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	// Function Destination.Run is called in a goroutine, we have to wait for it to
	// run to prove this works.
	closeCh := make(chan struct{})
	mockDestination.EXPECT().
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.DestinationRunStream) error {
			defer close(closeCh)
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Run")
	}
}

func testDestination_Start_Fail(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := cerrors.New("test error")

	mockDestination.EXPECT().
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	got := destination.Start(ctx)
	is.Equal(got.Error(), want.Error())
}

func testDestination_Write_Success(t *testing.T, tdf testDispenserFunc) {
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
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.DestinationRunStream) error {
			defer close(closeCh)
			got, err := stream.Recv()
			is.NoErr(err)
			if diff := cmp.Diff(got.Record, want); diff != "" {
				t.Errorf("expected ack: %s", diff)
			}
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)

	err = destination.Write(ctx, want)
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Write")
	}

	// wait for stream closing to propagate from plugin to Conduit
	time.Sleep(time.Millisecond * 50)

	err = destination.Write(ctx, opencdc.Record{})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testDestination_Write_WithoutStart(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, _ := tdf(t)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Write(ctx, opencdc.Record{})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testDestination_Ack_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	var want []opencdc.Position
	for i := 0; i < 10; i++ {
		want = append(want, []byte(fmt.Sprintf("position-%d", i)))
	}

	mockDestination.EXPECT().
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.DestinationRunStream) error {
			for _, p := range want {
				err := stream.Send(cpluginv2.DestinationRunResponse{
					AckPosition: p,
				})
				is.NoErr(err)
			}
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)

	var got []opencdc.Position
	for i := 0; i < len(want); i++ {
		pos, err := destination.Ack(ctx)
		is.NoErr(err)
		got = append(got, pos)
	}

	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("expected position: %s", diff)
	}
}

func testDestination_Ack_WithError(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	wantPos := opencdc.Position("test-position")
	wantErr := cerrors.New("test error")

	mockDestination.EXPECT().
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.DestinationRunStream) error {
			err := stream.Send(cpluginv2.DestinationRunResponse{
				AckPosition: wantPos,
				Error:       wantErr.Error(),
			})
			is.NoErr(err)
			return nil
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)

	gotPos, gotErr := destination.Ack(ctx)
	if diff := cmp.Diff(gotPos, wantPos); diff != "" {
		t.Errorf("expected position: %s", diff)
	}
	is.Equal(gotErr.Error(), wantErr.Error())
}

func testDestination_Ack_WithoutStart(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, _ := tdf(t)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	_, err = destination.Ack(ctx)
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
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream cpluginv2.DestinationRunStream) error {
			defer close(closeCh)
			_, _ = stream.Recv() // receive record and fail
			return want
		})

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)

	err = destination.Write(ctx, opencdc.Record{})
	is.NoErr(err)

	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Write")
	}

	// Error is returned through the Ack function, that's the incoming stream.
	_, err = destination.Ack(ctx)
	// Unwrap inner-most error
	var got error
	for unwrapped := err; unwrapped != nil; {
		got = unwrapped
		unwrapped = cerrors.Unwrap(unwrapped)
	}
	is.Equal(got.Error(), want.Error())

	// Write returns just a generic error
	err = destination.Write(ctx, opencdc.Record{})
	is.True(cerrors.Is(err, ErrStreamNotOpen))
}

func testDestination_Teardown_Success(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := cerrors.New("init error")
	closeCh := make(chan struct{})
	stopRunCh := make(chan struct{})
	mockDestination.EXPECT().
		Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
		Return(cpluginv2.DestinationStartResponse{}, nil)
	mockDestination.EXPECT().
		Stop(gomock.Any(), cpluginv2.DestinationStopRequest{}).
		Return(cpluginv2.DestinationStopResponse{}, nil)
	mockDestination.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, stream cpluginv2.DestinationRunStream) error {
			defer close(closeCh)
			<-stopRunCh
			return nil
		})
	mockDestination.EXPECT().
		Teardown(gomock.Any(), cpluginv2.DestinationTeardownRequest{}).
		Return(cpluginv2.DestinationTeardownResponse{}, want)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.Start(ctx)
	is.NoErr(err)
	err = destination.Stop(ctx, nil)
	is.NoErr(err)

	got := destination.Teardown(ctx)
	is.Equal(got.Error(), want.Error())

	close(stopRunCh)
	select {
	case <-closeCh:
	case <-time.After(time.Second):
		t.Fatal("should've received call to destination.Run")
	}
}

func testDestination_Lifecycle_OnCreated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := map[string]string{"foo": "bar"}

	mockDestination.EXPECT().
		LifecycleOnCreated(gomock.Any(), cpluginv2.DestinationLifecycleOnCreatedRequest{
			Config: want,
		}).
		Return(cpluginv2.DestinationLifecycleOnCreatedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.LifecycleOnCreated(ctx, want)
	is.NoErr(err)
}

func testDestination_Lifecycle_OnUpdated(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	wantBefore := map[string]string{"foo": "bar"}
	wantAfter := map[string]string{"foo": "baz"}

	mockDestination.EXPECT().
		LifecycleOnUpdated(gomock.Any(), cpluginv2.DestinationLifecycleOnUpdatedRequest{
			ConfigBefore: wantBefore,
			ConfigAfter:  wantAfter,
		}).
		Return(cpluginv2.DestinationLifecycleOnUpdatedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.LifecycleOnUpdated(ctx, wantBefore, wantAfter)
	is.NoErr(err)
}

func testDestination_Lifecycle_OnDeleted(t *testing.T, tdf testDispenserFunc) {
	is := is.New(t)
	ctx := context.Background()
	dispenser, _, _, mockDestination := tdf(t)

	want := map[string]string{"foo": "bar"}

	mockDestination.EXPECT().
		LifecycleOnDeleted(gomock.Any(), cpluginv2.DestinationLifecycleOnDeletedRequest{
			Config: want,
		}).
		Return(cpluginv2.DestinationLifecycleOnDeletedResponse{}, nil)

	destination, err := dispenser.DispenseDestination()
	is.NoErr(err)

	err = destination.LifecycleOnDeleted(ctx, want)
	is.NoErr(err)
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
				Configure(gomock.Any(), cpluginv2.DestinationConfigureRequest{}).
				Do(func(context.Context, cpluginv2.DestinationConfigureRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			return d.Configure(ctx, map[string]string{})
		},
	}, {
		name: "Start",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Start(gomock.Any(), cpluginv2.DestinationStartRequest{}).
				Do(func(context.Context, cpluginv2.DestinationStartRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			return d.Start(ctx)
		},
	}, {
		name: "Stop",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Stop(gomock.Any(), cpluginv2.DestinationStopRequest{}).
				Do(func(context.Context, cpluginv2.DestinationStopRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			return d.Stop(ctx, nil)
		},
	}, {
		name: "Teardown",
		prepareExpectation: func(m *mock.DestinationPlugin, blockUntil chan struct{}) {
			m.EXPECT().
				Teardown(gomock.Any(), cpluginv2.DestinationTeardownRequest{}).
				Do(func(context.Context, cpluginv2.DestinationTeardownRequest) {
					<-blockUntil
				})
		},
		callFn: func(ctx context.Context, d DestinationPlugin) error {
			return d.Teardown(ctx)
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
