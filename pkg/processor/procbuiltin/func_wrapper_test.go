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

package procbuiltin

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestFuncWrapper_InspectIn(t *testing.T) {
	ctx := context.Background()
	wantIn := record.Record{
		Position: record.Position("position-in"),
		Metadata: record.Metadata{"meta-key-in": "meta-value-in"},
		Key:      record.RawData{Raw: []byte("key-in")},
		Payload:  record.Change{After: record.RawData{Raw: []byte("payload-in")}},
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

			session, err := underTest.InspectIn(ctx)
			is.NoErr(err)
			_, _ = underTest.Process(ctx, wantIn)

			gotIn, got, err := cchan.Chan[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
			is.NoErr(err)
			is.True(got)
			is.Equal(wantIn, gotIn)
		})
	}
}

func TestFuncWrapper_InspectOut(t *testing.T) {
	ctx := context.Background()
	wantOut := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"meta-key-out": "meta-value-out"},
		Key:      record.RawData{Raw: []byte("key-out")},
		Payload:  record.Change{After: record.RawData{Raw: []byte("payload-out")}},
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
				return wantOut, tc.err
			})

			session, err := underTest.InspectOut(ctx)
			is.NoErr(err)
			_, _ = underTest.Process(ctx, record.Record{})

			gotOut, got, err := cchan.Chan[record.Record](session.C).RecvTimeout(ctx, 100*time.Millisecond)
			is.NoErr(err)
			is.True(got)
			is.Equal(wantOut, gotOut)
		})
	}
}
