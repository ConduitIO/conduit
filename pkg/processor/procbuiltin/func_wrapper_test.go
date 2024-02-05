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

package builtin

import (
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestFuncWrapper_InspectIn(t *testing.T) {
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

			underTest := NewFuncWrapper(func(_ context.Context, in record.Record) (record.Record, error) {
				return record.Record{}, tc.err
			})

			session := underTest.InspectIn(ctx, "test-id")
			_ = underTest.Process(ctx, wantIn)

			gotIn, got, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
			is.NoErr(err)
			is.True(got)
			is.Equal(wantIn, gotIn)
		})
	}
}

func TestFuncWrapper_InspectOut_Ok(t *testing.T) {
	ctx := context.Background()
	wantOut := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"meta-key-out": "meta-value-out"},
		Key:      record.RawData{Raw: []byte("key-out")},
		Payload:  record.Change{After: record.RawData{Raw: []byte("payload-out")}},
	}

	is := is.New(t)

	underTest := NewFuncWrapper(func(_ context.Context, in record.Record) (record.Record, error) {
		return wantOut, nil
	})

	session := underTest.InspectOut(ctx, "test-id")
	_ = underTest.Process(ctx, []opencdc.Record{})

	gotOut, got, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(got)
	is.Equal(wantOut, gotOut)
}

func TestFuncWrapper_InspectOut_ProcessingFailed(t *testing.T) {
	ctx := context.Background()
	wantOut := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"meta-key-out": "meta-value-out"},
		Key:      record.RawData{Raw: []byte("key-out")},
		Payload:  record.Change{After: record.RawData{Raw: []byte("payload-out")}},
	}

	is := is.New(t)

	underTest := NewFuncWrapper(func(_ context.Context, in record.Record) (record.Record, error) {
		return wantOut, cerrors.New("shouldn't happen")
	})

	session := underTest.InspectOut(ctx, "test-id")
	_ = underTest.Process(ctx, []opencdc.Record{})

	_, _, err := cchan.ChanOut[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
}

func TestFuncWrapper_Close(t *testing.T) {
	ctx := context.Background()

	is := is.New(t)

	underTest := NewFuncWrapper(func(_ context.Context, in record.Record) (record.Record, error) {
		return record.Record{}, nil
	})

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
