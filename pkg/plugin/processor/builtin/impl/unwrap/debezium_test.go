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

package unwrap

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

func TestDebeziumProcessor_Configure(t *testing.T) {
	testCases := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name:    "optional not provided",
			config:  config.Config{},
			wantErr: "",
		},
		{
			name:    "valid field (within .Payload)",
			config:  config.Config{"field": ".Payload.After.something"},
			wantErr: "",
		},
		{
			name:    "invalid field",
			config:  config.Config{"field": ".Key"},
			wantErr: `config invalid: error validating "field": ".Key" should match the regex "^.Payload": regex validation failed`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			err := NewDebeziumProcessor(log.Test(t)).Configure(context.Background(), tc.config)
			if tc.wantErr != "" {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			} else {
				is.NoErr(err)
			}
		})
	}
}

func TestDebeziumProcessor_Process(t *testing.T) {
	testCases := []struct {
		name    string
		config  config.Config
		record  opencdc.Record
		want    sdk.ProcessedRecord
		wantErr string
	}{
		{
			name:   "raw payload",
			config: config.Config{"field": ".Payload.After"},
			record: opencdc.Record{
				Metadata: map[string]string{},
				Key:      opencdc.RawData(`{"payload":"27"}`),
				Position: []byte("position"),
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.RawData(`{
		 "payload": {
		   "after": {
		     "description": "test1",
		     "id": 27
		   },
		   "before": null,
		   "op": "c",
		   "source": {
		     "opencdc.readAt": "1674061777225877000",
		     "opencdc.version": "v1"
		   },
		   "transaction": null,
		   "ts_ms": 1674061777225
		 },
		 "schema": {} 
		}`),
				},
			},
			want: sdk.SingleRecord{
				Position:  opencdc.Position("position"),
				Key:       opencdc.RawData("27"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225877000",
					"opencdc.version": "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.StructuredData{"description": "test1", "id": float64(27)},
				},
			},
		},
		{
			name:   "structured payload",
			config: config.Config{"field": ".Payload.After"},
			record: opencdc.Record{
				Metadata: map[string]string{
					"conduit.version": "v0.4.0",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"payload": map[string]any{
							"after": map[string]any{
								"description": "test1",
								"id":          27,
							},
							"before": nil,
							"op":     "u",
							"source": map[string]any{
								"opencdc.version": "v1",
							},
							"transaction": nil,
							"ts_ms":       float64(1674061777225),
						},
						"schema": map[string]any{},
					},
				},
				Key: opencdc.StructuredData{
					"payload": 27,
					"schema":  map[string]any{},
				},
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
					"conduit.version": "v0.4.0",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.StructuredData{"description": "test1", "id": 27},
				},
				Key: opencdc.RawData("27"),
			},
		},
		{
			name:   "structured data, payload missing",
			config: config.Config{"field": ".Payload.After"},
			record: opencdc.Record{
				Metadata: map[string]string{
					"conduit.version": "v0.4.0",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo":    "bar",
						"schema": map[string]any{},
					},
				},
				Key: opencdc.StructuredData{
					"payload": 27,
					"schema":  map[string]any{},
				},
			},
			want: sdk.ErrorRecord{
				Error: cerrors.New("data to be unwrapped doesn't contain a payload field"),
			},
		},
		{
			name:   "custom field, structured payload",
			config: config.Config{"field": ".Payload.After[\"debezium_event\"]"},
			record: opencdc.Record{
				Metadata: map[string]string{
					"conduit.version": "v0.4.0",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"debezium_event": map[string]any{
							"payload": map[string]any{
								"after": map[string]any{
									"description": "test1",
									"id":          27,
								},
								"before": nil,
								"op":     "u",
								"source": map[string]any{
									"opencdc.version": "v1",
								},
								"transaction": nil,
								"ts_ms":       float64(1674061777225),
							},
							"schema": map[string]any{},
						},
					},
				},
				Key: opencdc.StructuredData{
					"payload": 27,
					"schema":  map[string]any{},
				},
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
					"conduit.version": "v0.4.0",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.StructuredData{"description": "test1", "id": 27},
				},
				Key: opencdc.RawData("27"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := NewDebeziumProcessor(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			is.NoErr(err)

			gotSlice := underTest.Process(context.Background(), []opencdc.Record{tc.record})
			is.Equal(1, len(gotSlice))
			is.Equal("", cmp.Diff(tc.want, gotSlice[0], internal.CmpProcessedRecordOpts...))
		})
	}
}
