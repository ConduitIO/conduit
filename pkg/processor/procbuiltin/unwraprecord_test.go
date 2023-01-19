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

	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

var Dbz = "{" +
	"\"payload\":" +
	"{" +
	"\"after\":{\"description\":\"test1\",\"id\":27}," +
	"\"before\":null,\"op\":\"c\"," +
	"\"source\":" +
	"{" +
	"\"conduit.source.connector.id\":\"pg-to-kafka:pg\"," +
	"\"opencdc.readAt\":\"1674061777225877000\"," +
	"\"opencdc.version\":\"v1\"," +
	"\"postgres.table\":\"stuff\"" +
	"}," +
	"\"transaction\":null," +
	"\"ts_ms\":1674061777225" +
	"}," +
	"\"schema\":{}" +
	"}"

func TestUnwrapRecord_Config(t *testing.T) {
	tests := []struct {
		name    string
		config  processor.Config
		wantErr bool
	}{
		{
			name:    "empty config",
			config:  processor.Config{},
			wantErr: true,
		},
		{
			name: "invalid config",
			config: processor.Config{
				Settings: map[string]string{"foo": "bar"},
			},
			wantErr: true,
		},
		{
			name: "invalid config",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnwrapRecordPayload(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnwrapRecordPayload() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestUnwrapRecord_Process(t *testing.T) {
	is := is.New(t)

	type args struct {
		r record.Record
	}
	tests := []struct {
		name    string
		args    args
		want    record.Record
		config  processor.Config
		wantErr bool
	}{
		{
			name: "raw payload",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			args: args{r: record.Record{
				Payload: record.Change{
					Before: nil,
					After: record.RawData{
						Raw: []byte(Dbz),
					},
				},
			},
			},
			want: record.Record{
				Operation: record.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "pg-to-kafka:pg",
					"opencdc.readAt":              "1674061777225877000",
					"opencdc.version":             "v1",
					"postgres.table":              "stuff",
				},
				Payload: record.Change{
					Before: record.StructuredData(nil),
					After:  record.StructuredData{"description": "test1", "id": float64(27)},
				},
			},
			wantErr: false,
		},
		{
			name: "structured payload",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			args: args{r: record.Record{
				Payload: record.Change{
					Before: nil,
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"after": map[string]interface{}{
								"description": "test1",
								"id":          27,
							},
							"before": interface{}(nil),
							"op":     "u",
							"source": map[string]interface{}{
								"conduit.source.connector.id": "pg-to-kafka:pg",
								"opencdc.readAt":              "1674061777225877000",
								"opencdc.version":             "v1",
								"postgres.table":              "stuff",
							},
							"transaction": interface{}(nil),
							"ts_ms":       1.674061777225e+12,
						},
						"schema": map[string]interface{}{},
					},
				},
			},
			},
			want: record.Record{
				Operation: record.OperationUpdate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "pg-to-kafka:pg",
					"opencdc.readAt":              "1674061777225877000",
					"opencdc.version":             "v1",
					"postgres.table":              "stuff",
				},
				Payload: record.Change{
					Before: record.StructuredData(nil),
					After:  record.StructuredData{"description": "test1", "id": 27},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underTest, err := UnwrapRecordPayload(tt.config)
			is.NoErr(err)
			got, err := underTest.Process(context.Background(), tt.args.r)

			if (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}
