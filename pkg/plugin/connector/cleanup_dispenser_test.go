// Copyright © 2024 Meroxa, Inc.
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

package connector_test

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestCleanupDispenser_DispenseSource_Fail(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	called := false
	wantErr := cerrors.New("test error")

	targetDispenser := mock.NewDispenser(ctrl)
	targetSource := mock.NewSourcePlugin(ctrl)
	targetDispenser.EXPECT().
		DispenseSource().
		Return(targetSource, wantErr)

	underTest := connector.DispenserWithCleanup(targetDispenser, func() { called = true })

	_, err := underTest.DispenseSource()
	is.True(cerrors.Is(err, wantErr))
	is.True(called) // a failed dispense is never torn down, so the token is revoked now
}

func TestCleanupDispenser_Source_Teardown(t *testing.T) {
	testCases := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "teardown success",
			wantErr: nil,
		},
		{
			name:    "teardown error",
			wantErr: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			called := false

			targetDispenser := mock.NewDispenser(ctrl)
			targetSource := mock.NewSourcePlugin(ctrl)
			targetDispenser.EXPECT().
				DispenseSource().
				Return(targetSource, nil)
			targetSource.EXPECT().
				Teardown(gomock.Any(), gomock.Any()).
				Return(pconnector.SourceTeardownResponse{}, tc.wantErr)

			underTest := connector.DispenserWithCleanup(targetDispenser, func() { called = true })

			source, err := underTest.DispenseSource()
			is.NoErr(err)

			_, err = source.Teardown(ctx, pconnector.SourceTeardownRequest{})
			if tc.wantErr != nil {
				is.NoErr(err)
			} else {
				is.True(cerrors.Is(err, tc.wantErr))
			}

			is.True(called)
		})
	}
}

func TestCleanupDispenser_DispenseDestination_Fail(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	called := false
	wantErr := cerrors.New("test error")

	targetDispenser := mock.NewDispenser(ctrl)
	targetDestination := mock.NewDestinationPlugin(ctrl)
	targetDispenser.EXPECT().
		DispenseDestination().
		Return(targetDestination, wantErr)

	underTest := connector.DispenserWithCleanup(targetDispenser, func() { called = true })

	_, err := underTest.DispenseDestination()
	is.True(cerrors.Is(err, wantErr))
	is.True(called) // a failed dispense is never torn down, so the token is revoked now
}

func TestCleanupDispenser_Destination_Teardown(t *testing.T) {
	testCases := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "teardown success",
			wantErr: nil,
		},
		{
			name:    "teardown error",
			wantErr: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			called := false

			targetDispenser := mock.NewDispenser(ctrl)
			targetDestination := mock.NewDestinationPlugin(ctrl)
			targetDispenser.EXPECT().
				DispenseDestination().
				Return(targetDestination, nil)
			targetDestination.EXPECT().
				Teardown(gomock.Any(), gomock.Any()).
				Return(pconnector.DestinationTeardownResponse{}, tc.wantErr)

			underTest := connector.DispenserWithCleanup(targetDispenser, func() { called = true })

			source, err := underTest.DispenseDestination()
			is.NoErr(err)

			_, err = source.Teardown(ctx, pconnector.DestinationTeardownRequest{})
			if tc.wantErr != nil {
				is.NoErr(err)
			} else {
				is.True(cerrors.Is(err, tc.wantErr))
			}

			is.True(called)
		})
	}
}

// TestDispenserWithCleanup_ForwardsSpecification: the plugin service wraps
// every registry dispenser for token cleanup; the wrapper must keep the
// specification visible (#3321), and report none for a dispenser without one.
func TestDispenserWithCleanup_ForwardsSpecification(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	plain := mock.NewDispenser(ctrl)
	_, ok := connector.DispenserSpecification(connector.DispenserWithCleanup(plain, func() {}))
	is.True(!ok)

	want := pconnector.Specification{Name: "with-spec"}
	_, ok = connector.DispenserSpecification(plain)
	is.True(!ok)
	got, ok := connector.DispenserSpecification(connector.DispenserWithCleanup(specCarrying{Dispenser: plain, spec: want}, func() {}))
	is.True(ok)
	is.Equal(got, want)
}

type specCarrying struct {
	connector.Dispenser
	spec pconnector.Specification
}

func (d specCarrying) Specification() (pconnector.Specification, bool) { return d.spec, true }
