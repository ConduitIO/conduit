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

package base64

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestEncodeProcessor_Success(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name   string
		field  string
		record opencdc.Record
		want   sdk.SingleRecord
	}{{
		name:  "encode raw data",
		field: ".Key",
		record: opencdc.Record{
			Key: opencdc.RawData("foo"),
		},
		want: sdk.SingleRecord{
			Key: opencdc.RawData("Zm9v"),
		},
	}, {
		name:  "encode string",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": "bar",
			},
		},
		want: sdk.SingleRecord{
			Key: opencdc.StructuredData{
				"foo": "YmFy",
			},
		},
	}, {
		name:  "encode int",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": 1,
			},
		},
		want: sdk.SingleRecord{
			Key: opencdc.StructuredData{
				"foo": "MQ==",
			},
		},
	}, {
		name:  "encode float",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": 1.1,
			},
		},
		want: sdk.SingleRecord{
			Key: opencdc.StructuredData{
				"foo": "MS4x",
			},
		},
	}, {
		name:  "encode bool",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": true,
			},
		},
		want: sdk.SingleRecord{
			Key: opencdc.StructuredData{
				"foo": "dHJ1ZQ==",
			},
		},
	}, {
		name:  "encode []byte",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": []byte("bar"),
			},
		},
		want: sdk.SingleRecord{
			Key: opencdc.StructuredData{
				"foo": "YmFy",
			},
		},
	}, {
		name:   "encode nil",
		field:  ".Key",
		record: opencdc.Record{},
		want:   sdk.SingleRecord{},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc := NewEncodeProcessor(log.Nop())
			err := proc.Configure(ctx, config.Config{"field": tc.field})
			is.NoErr(err)
			got := proc.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(tc.want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}

func TestEncodeProcessor_Fail(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name    string
		field   string
		record  opencdc.Record
		wantErr error
	}{{
		name:  "encode structured data",
		field: ".Key",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": "bar",
			},
		},
		wantErr: cerrors.New("unexpected data type opencdc.StructuredData"),
	}, {
		name:  "encode map",
		field: ".Key.foo",
		record: opencdc.Record{
			Key: opencdc.StructuredData{
				"foo": map[string]any{
					"bar": "baz",
				},
			},
		},
		wantErr: cerrors.New("unexpected data type map[string]interface {}"),
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc := NewEncodeProcessor(log.Nop())
			err := proc.Configure(ctx, config.Config{"field": tc.field})
			is.NoErr(err)
			got := proc.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(sdk.ErrorRecord{Error: tc.wantErr}, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}
