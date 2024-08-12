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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestDecodeJSON_Process(t *testing.T) {
	proc := NewDecodeProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name   string
		config config.Config
		record opencdc.Record
		want   sdk.ProcessedRecord
	}{
		{
			name:   "raw key to structured",
			config: config.Config{"field": ".Key"},
			record: opencdc.Record{
				Key: opencdc.RawData(`{"after":{"data":4,"id":3}}`),
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{
					"after": map[string]any{"data": float64(4), "id": float64(3)},
				},
			},
		}, {
			name:   "raw payload.after.foo to structured",
			config: config.Config{"field": ".Payload.After.foo"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": `{"after":{"data":4,"id":3},"baz":"bar"}`,
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": map[string]any{
							"after": map[string]any{"data": float64(4), "id": float64(3)},
							"baz":   "bar",
						},
					},
				},
			},
		}, {
			name:   "slice payload.after.foo to structured",
			config: config.Config{"field": ".Payload.After.foo"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": `["one", "two", "three"]`,
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": []any{"one", "two", "three"},
					},
				},
			},
		}, {
			name:   "string JSON value payload.after.foo to structured",
			config: config.Config{"field": ".Payload.After.foo"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": `"bar"`,
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo": "bar",
					},
				},
			},
		}, {
			name:   "raw payload.before to structured",
			config: config.Config{"field": ".Payload.Before"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.RawData(`{"before":{"data":4},"foo":"bar"}`),
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"before": map[string]any{"data": float64(4)},
						"foo":    "bar",
					},
				},
			},
		}, {
			name:   "already structured data",
			config: config.Config{"field": ".Key"},
			record: opencdc.Record{
				Key: opencdc.StructuredData{
					"after": map[string]any{"data": float64(4), "id": float64(3)},
				},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{
					"after": map[string]any{"data": float64(4), "id": float64(3)},
				},
			},
		}, {
			name:   "empty raw data converted to empty structured data",
			config: config.Config{"field": ".Key"},
			record: opencdc.Record{
				Key: opencdc.RawData(""),
			},
			want: sdk.SingleRecord{
				Key: nil,
			},
		}, {
			name:   "nil value",
			config: config.Config{"field": ".Payload.After"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: nil,
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: nil,
				},
			},
		}, {
			name:   "invalid json",
			config: config.Config{"field": ".Key"},
			record: opencdc.Record{
				Key: opencdc.RawData(`"invalid":"json"`),
			},
			want: sdk.ErrorRecord{Error: cerrors.New("failed to unmarshal raw data as JSON: invalid character ':' after top-level value")},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.config)
			is.NoErr(err)
			got := proc.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(tc.want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}

func TestDecodeJSON_Configure(t *testing.T) {
	proc := NewDecodeProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		config  config.Config
		wantErr bool
	}{
		{
			name:    "valid field, key",
			config:  config.Config{"field": ".Key"},
			wantErr: false,
		},
		{
			name:    "valid field, payload.after",
			config:  config.Config{"field": ".Payload.After"},
			wantErr: false,
		},
		{
			name:    "valid field, payload.before",
			config:  config.Config{"field": ".Payload.Before"},
			wantErr: false,
		},
		{
			name:    "valid field, payload.after.foo",
			config:  config.Config{"field": ".Payload.After.foo"},
			wantErr: false,
		},
		{
			name:    "invalid config, missing param",
			config:  config.Config{},
			wantErr: true,
		},
		{
			name:    "invalid field .Metadata",
			config:  config.Config{"field": ".Metadata"},
			wantErr: true,
		},
		{
			name:    "invalid field, .Payload",
			config:  config.Config{"field": ".Payload"},
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
