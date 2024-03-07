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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestRunnableProcessor_Open(t *testing.T) {
	ctx := context.Background()
	inst := &Instance{
		Config: Config{
			Settings: map[string]string{
				"foo": "bar",
			},
			Workers: 123,
		},
	}

	testCases := []struct {
		name    string
		cfgErr  error
		openErr error
		wantErr error
	}{
		{
			name:    "success",
			cfgErr:  nil,
			openErr: nil,
			wantErr: nil,
		},
		{
			name:    "configuration error",
			cfgErr:  cerrors.New("config is wrong"),
			openErr: nil,
			wantErr: cerrors.New("failed configuring processor: config is wrong"),
		},
		{
			name:    "open error",
			cfgErr:  nil,
			openErr: cerrors.New("open method exploded"),
			wantErr: cerrors.New("failed opening processor: open method exploded"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().
				Configure(gomock.Any(), inst.Config.Settings).
				Return(tc.cfgErr)
			// If there was a configuration error,
			// then Open() should not be called.
			if tc.cfgErr == nil {
				proc.EXPECT().Open(gomock.Any()).Return(tc.openErr)
			}

			underTest := newRunnableProcessor(proc, nil, inst)
			err := underTest.Open(ctx)
			if tc.wantErr == nil {
				is.NoErr(err)
			} else {
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

func TestRunnableProcessor_Teardown(t *testing.T) {
	ctx := context.Background()
	inst := &Instance{
		Config: Config{
			Settings: map[string]string{
				"foo": "bar",
			},
			Workers: 123,
		},
	}

	testCases := []struct {
		name        string
		teardownErr error
		wantErr     error
	}{
		{
			name:        "no error",
			teardownErr: nil,
			wantErr:     nil,
		},
		{
			name:        "with error",
			teardownErr: cerrors.New("boom!"),
			wantErr:     cerrors.New("boom!"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			proc := mock.NewProcessor(gomock.NewController(t))
			proc.EXPECT().Teardown(gomock.Any()).Return(tc.teardownErr)

			underTest := newRunnableProcessor(proc, nil, inst)
			err := underTest.Teardown(ctx)
			if tc.wantErr == nil {
				is.NoErr(err)
			} else {
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

func TestRunnableProcessor_ProcessedRecordsInspected(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Key: opencdc.RawData("test key in"),
		},
	}
	recsOut := []sdk.ProcessedRecord{
		sdk.SingleRecord{
			Key: opencdc.RawData("test key out"),
		},
	}
	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().Process(gomock.Any(), recsIn).Return(recsOut)

	underTest := newRunnableProcessor(proc, nil, inst)
	inSession := underTest.inInsp.NewSession(ctx, "id-in")
	outSession := underTest.outInsp.NewSession(ctx, "id-out")

	_ = underTest.Process(ctx, recsIn)
	defer underTest.Close()

	rec, gotRec, err := cchan.ChanOut[record.Record](inSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(gotRec)
	is.NoErr(err)
	is.Equal(record.FromOpenCDC(recsIn[0]), rec)

	rec, gotRec, err = cchan.ChanOut[record.Record](outSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(gotRec)
	is.NoErr(err)
	is.Equal(recsOut[0], sdk.SingleRecord(rec.ToOpenCDC()))
}

func TestRunnableProcessor_FilteredRecordsNotInspected(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Key: opencdc.RawData("test key in"),
		},
	}

	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().Process(gomock.Any(), recsIn).Return([]sdk.ProcessedRecord{sdk.FilterRecord{}})

	underTest := newRunnableProcessor(proc, nil, inst)
	inSession := underTest.inInsp.NewSession(ctx, "id-in")
	outSession := underTest.outInsp.NewSession(ctx, "id-out")

	_ = underTest.Process(ctx, recsIn)
	defer underTest.Close()

	rec, gotRec, err := cchan.ChanOut[record.Record](inSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(gotRec)
	is.NoErr(err)
	is.Equal(record.FromOpenCDC(recsIn[0]), rec)

	_, gotRec, err = cchan.ChanOut[record.Record](outSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(!gotRec)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
}

func TestRunnableProcessor_ErrorRecordsNotInspected(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Key: opencdc.RawData("test key in"),
		},
	}

	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().Process(gomock.Any(), recsIn).Return([]sdk.ProcessedRecord{sdk.ErrorRecord{}})

	underTest := newRunnableProcessor(proc, nil, inst)
	inSession := underTest.inInsp.NewSession(ctx, "id-in")
	outSession := underTest.outInsp.NewSession(ctx, "id-out")

	_ = underTest.Process(ctx, recsIn)
	defer underTest.Close()

	rec, gotRec, err := cchan.ChanOut[record.Record](inSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(gotRec)
	is.NoErr(err)
	is.Equal(record.FromOpenCDC(recsIn[0]), rec)

	_, gotRec, err = cchan.ChanOut[record.Record](outSession.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(!gotRec)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
}

func TestRunnableProcessor_Process_ConditionNotMatching(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Metadata: opencdc.Metadata{"key": "something"},
		},
	}

	proc := mock.NewProcessor(gomock.NewController(t))

	condition, err := newProcessorCondition(`{{ eq .Metadata.key "val" }}`)
	is.NoErr(err)
	underTest := newRunnableProcessor(
		proc,
		condition,
		inst,
	)

	recsOut := underTest.Process(ctx, recsIn)
	defer underTest.Close()

	is.Equal([]sdk.ProcessedRecord{sdk.SingleRecord(recsIn[0])}, recsOut)
}

func TestRunnableProcessor_Process_ConditionMatching(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Metadata: opencdc.Metadata{"key": "val"},
		},
	}

	wantRecs := []sdk.ProcessedRecord{sdk.SingleRecord{Key: opencdc.RawData(`a key`)}}
	proc := mock.NewProcessor(gomock.NewController(t))
	proc.EXPECT().Process(ctx, recsIn).Return(wantRecs)

	condition, err := newProcessorCondition(`{{ eq .Metadata.key "val" }}`)
	is.NoErr(err)
	underTest := newRunnableProcessor(
		proc,
		condition,
		inst,
	)

	gotRecs := underTest.Process(ctx, recsIn)
	defer underTest.Close()

	is.Equal(wantRecs, gotRecs)
}

func TestRunnableProcessor_Process_ConditionError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inst := newTestInstance()
	recsIn := []opencdc.Record{
		{
			Metadata: opencdc.Metadata{"key": "val"},
		},
	}

	proc := mock.NewProcessor(gomock.NewController(t))

	condition, err := newProcessorCondition("junk")
	is.NoErr(err)
	underTest := newRunnableProcessor(
		proc,
		condition,
		inst,
	)

	gotRecs := underTest.Process(ctx, recsIn)
	defer underTest.Close()

	is.Equal(1, len(gotRecs))
	gotRec, gotErr := gotRecs[0].(sdk.ErrorRecord)
	is.True(gotErr)
	is.Equal(
		"failed evaluating condition: error converting the condition go-template output to boolean, "+
			"strconv.ParseBool: parsing \"junk\": invalid syntax: strconv.ParseBool: parsing \"junk\": "+
			"invalid syntax",
		gotRec.Error.Error(),
	)

}

func newTestInstance() *Instance {
	return &Instance{
		Config: Config{
			Settings: map[string]string{
				"foo": "bar",
			},
			Workers: 123,
		},

		inInsp:  inspector.New(log.Nop(), inspector.DefaultBufferSize),
		outInsp: inspector.New(log.Nop(), inspector.DefaultBufferSize),
	}
}
