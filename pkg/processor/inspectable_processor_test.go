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

package processor

import (
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/processor/mock"
	"go.uber.org/mock/gomock"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestInspectableProcessor_Decorate_Open(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name string
		err  error
	}{
		{
			name: "returns ok",
		},
		{
			name: "returns an error",
			err:  cerrors.New("boom!"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().Open(gomock.Any()).Return(tc.err)
			underTest := newInspectableProcessor(proc, log.Nop())
			is.Equal(tc.err, underTest.Open(ctx))
		})
	}
}

func TestInspectableProcessor_Decorate_Configure(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name string
		err  error
	}{
		{
			name: "returns ok",
		},
		{
			name: "returns an error",
			err:  cerrors.New("boom!"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			cfg := map[string]string{
				"url": "conduit.io",
			}

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().Configure(gomock.Any(), cfg).Return(tc.err)

			underTest := newInspectableProcessor(proc, log.Nop())
			is.Equal(tc.err, underTest.Configure(ctx, cfg))
		})
	}
}

func TestInspectableProcessor_Decorate_Process(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)

	wantIn := []opencdc.Record{
		{
			Position: opencdc.Position("position-in"),
			Metadata: opencdc.Metadata{"meta-key-in": "meta-value-in"},
			Key:      opencdc.RawData("key-in"),
			Payload:  opencdc.Change{After: opencdc.RawData("payload-in")},
		},
	}
	wantOut := []sdk.ProcessedRecord{
		sdk.SingleRecord{
			Position: opencdc.Position("position-in"),
			Metadata: opencdc.Metadata{"meta-key-in": "meta-value-in"},
			Key:      opencdc.RawData("key-in"),
			Payload:  opencdc.Change{After: opencdc.RawData("payload-in")},
		},
	}

	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().Process(gomock.Any(), wantIn).Return(wantOut)

	underTest := newInspectableProcessor(proc, log.Nop())
	is.Equal(wantOut, underTest.Process(ctx, wantIn))
}

func TestInspectableProcessor_Decorate_Teardown(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name string
		err  error
	}{
		{
			name: "returns ok",
		},
		{
			name: "returns an error",
			err:  cerrors.New("boom!"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().Teardown(gomock.Any()).Return(tc.err)

			underTest := newInspectableProcessor(proc, log.Nop())
			is.Equal(tc.err, underTest.Teardown(ctx))
		})
	}
}

func TestInspectableProcessor_InspectIn(t *testing.T) {
	ctx := context.Background()
	wantIn := []opencdc.Record{
		{
			Position: opencdc.Position("position-in"),
			Metadata: opencdc.Metadata{"meta-key-in": "meta-value-in"},
			Key:      opencdc.RawData("key-in"),
			Payload:  opencdc.Change{After: opencdc.RawData("payload-in")},
		},
	}
	testCases := []struct {
		name string
		err  error
	}{
		{
			name: "processing ok",
			err:  nil,
		},
		{
			name: "processing failed",
			err:  cerrors.New("shouldn't happen"),
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().
				Process(gomock.Any(), wantIn).
				Return(nil)
			underTest := newInspectableProcessor(proc, log.Nop())

			session := underTest.InspectIn(ctx, "test-id")
			_ = underTest.Process(ctx, wantIn)

			gotIn, got, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
			is.NoErr(err)
			is.True(got)
			is.Equal(wantIn[0], gotIn.ToOpenCDC())
		})
	}
}

func TestInspectableProcessor_InspectOut_Ok(t *testing.T) {
	ctx := context.Background()
	wantOut := []sdk.ProcessedRecord{
		sdk.SingleRecord{
			Position: opencdc.Position("position-out"),
			Metadata: opencdc.Metadata{"meta-key-out": "meta-value-out"},
			Key:      opencdc.RawData("key-out"),
			Payload:  opencdc.Change{After: opencdc.RawData("payload-out")},
		},
	}

	is := is.New(t)

	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		Return(wantOut)
	underTest := newInspectableProcessor(proc, log.Nop())

	session := underTest.InspectOut(ctx, "test-id")
	_ = underTest.Process(ctx, nil)

	gotOut, got, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(got)
	is.Equal(wantOut[0], sdk.SingleRecord(gotOut.ToOpenCDC()))
}

func TestInspectableProcessor_InspectOut_ProcessingFailed(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)

	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		Return([]sdk.ProcessedRecord{sdk.ErrorRecord{Error: cerrors.New("shouldn't happen")}})

	underTest := newInspectableProcessor(proc, log.Nop())

	session := underTest.InspectOut(ctx, "test-id")
	_ = underTest.Process(ctx, nil)

	_, _, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
}

func TestInspectableProcessor_Close(t *testing.T) {
	ctx := context.Background()

	is := is.New(t)

	proc := mock.NewProcessor(gomock.NewController(t))
	underTest := newInspectableProcessor(proc, log.Nop())

	in := underTest.InspectIn(ctx, "test-id")
	out := underTest.InspectOut(ctx, "test-id")
	underTest.Close()

	// incoming records session should be closed
	_, got, err := cchan.ChanOut[record.Record](in.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(!got)

	// outgoing records session should be closed
	_, got, err = cchan.ChanOut[record.Record](out.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(!got)
}
