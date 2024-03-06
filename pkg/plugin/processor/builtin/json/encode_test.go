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

package json

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestEncode_Process(t *testing.T) {
	proc := newEncodeProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name   string
		config map[string]string
		record opencdc.Record
		want   sdk.ProcessedRecord
	}{
		{
			name:   "structured key to raw data",
			config: map[string]string{"field": ".Key"},
			record: opencdc.Record{
				Key: opencdc.StructuredData{
					"after": map[string]any{"data": float64(4), "id": float64(3)},
				},
			},
			want: sdk.SingleRecord{
				Key: opencdc.RawData(`{"after":{"data":4,"id":3}}`),
			},
		}, {
			name:   "encode payload.after.foo map",
			config: map[string]string{"field": ".Payload.After.foo"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": map[string]any{
							"after": map[string]any{"data": float64(4), "id": float64(3)},
							"baz":   "bar",
						},
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": []uint8(`{"after":{"data":4,"id":3},"baz":"bar"}`),
					},
				},
			},
		}, {
			name:   "slice under payload.after to raw",
			config: map[string]string{"field": ".Payload.After"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": []any{"one", "two", "three"},
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.RawData(`{"foo":["one","two","three"]}`),
				},
			},
		}, {
			name:   "encode int value",
			config: map[string]string{"field": ".Payload.After.foo"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": 123,
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": []uint8("123"),
					},
				},
			},
		}, {
			name:   "nil value",
			config: map[string]string{"field": ".Payload.After"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: nil,
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.RawData("null"),
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.config)
			is.NoErr(err)
			got := proc.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			// todo delete line after merging into refactoring PR
			is.Equal(tc.want, got[0])
			// todo uncomment when merged into refactoring PR
			//is.Equal("", cmp.Diff(tc.want, got[0], internal.cmpProcessedRecordOpts...))
		})
	}
}

func TestEncode_Configure(t *testing.T) {
	proc := newEncodeProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		config  map[string]string
		wantErr bool
	}{
		{
			name:    "valid field, key",
			config:  map[string]string{"field": ".Key"},
			wantErr: false,
		},
		{
			name:    "valid field, payload.after",
			config:  map[string]string{"field": ".Payload.After"},
			wantErr: false,
		},
		{
			name:    "valid field, payload.after.foo",
			config:  map[string]string{"field": ".Payload.After.foo"},
			wantErr: false,
		},
		{
			name:    "invalid config, missing param",
			config:  map[string]string{},
			wantErr: true,
		},
		{
			name:    "invalid field .Metadata",
			config:  map[string]string{"field": ".Metadata"},
			wantErr: true,
		},
		{
			name:    "invalid field, .Payload",
			config:  map[string]string{"field": ".Payload"},
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.config)
			if tc.wantErr {
				is.True(err != nil)
				return
			}
			is.NoErr(err)
		})
	}
}
