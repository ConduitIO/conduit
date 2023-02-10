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

const DebeziumRecord = `{
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
		}`

func TestUnwrap_Config(t *testing.T) {
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
				Settings: map[string]string{"format": "bar"},
			},
			wantErr: true,
		},
		{
			name: "valid debezium config",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			wantErr: false,
		},
		{
			name: "valid kafka-connect config",
			config: processor.Config{
				Settings: map[string]string{"format": "kafka-connect"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unwrap(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Unwrap() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestUnwrap_Process(t *testing.T) {
	tests := []struct {
		name    string
		record  record.Record
		want    record.Record
		config  processor.Config
		wantErr bool
	}{
		{
			name: "raw payload",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Key: record.RawData{
					Raw: []byte(`{"payload":"id"}`),
				},
				Position: []byte("position"),
				Payload: record.Change{
					Before: nil,
					After: record.RawData{
						Raw: []byte(DebeziumRecord),
					},
				},
			},
			want: record.Record{
				Operation: record.OperationCreate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225877000",
					"opencdc.version": "v1",
				},
				Key: record.RawData{
					Raw: []byte("id"),
				},
				Position: []byte("position"),
				Payload: record.Change{
					Before: nil,
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
			record: record.Record{
				Metadata: map[string]string{
					"conduit.version": "v0.4.0",
				},
				Payload: record.Change{
					Before: nil,
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"after": map[string]interface{}{
								"description": "test1",
								"id":          27,
							},
							"before": nil,
							"op":     "u",
							"source": map[string]interface{}{
								"opencdc.version": "v1",
							},
							"transaction": nil,
							"ts_ms":       float64(1674061777225),
						},
						"schema": map[string]interface{}{},
					},
				},
				Key: record.StructuredData{
					"payload": 27,
					"schema":  map[string]interface{}{},
				},
			},
			want: record.Record{
				Operation: record.OperationUpdate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
					"conduit.version": "v0.4.0",
				},
				Payload: record.Change{
					Before: nil,
					After:  record.StructuredData{"description": "test1", "id": 27},
				},
				Key: record.RawData{
					Raw: []byte("27"),
				},
			},
			wantErr: false,
		},
		{
			name: "structured payload kafka-connect",
			config: processor.Config{
				Settings: map[string]string{"format": "kafka-connect"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Payload: record.Change{
					Before: record.StructuredData(nil),
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"description": "test2",
							"id":          27,
						},
						"schema": map[string]interface{}{},
					},
				},
				Key: record.StructuredData{
					"payload": map[string]interface{}{
						"id": 27,
					},
					"schema": map[string]interface{}{},
				},
			},
			want: record.Record{
				Operation: record.OperationSnapshot,
				Payload: record.Change{
					After: record.StructuredData{"description": "test2", "id": 27},
				},
				Key: record.StructuredData{"id": 27},
			},
			wantErr: false,
		},
		{
			name: "payload is invalid JSON",
			config: processor.Config{
				Settings: map[string]string{"format": "kafka-connect"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Payload: record.Change{
					Before: nil,
					After: record.RawData{
						Raw:    []byte("\"invalid\":\"true\""),
						Schema: nil,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "mongoDB debezium record",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Key:      record.RawData{Raw: []byte(`{ "payload": { "id": "{ \"$oid\" : \"63210f1a3bc50864fde46a84\"}" }, "schema": { "fields": [ { "field": "id", "optional": false, "type": "string" } ], "name": "resource_7_735174.demo.user.Key", "optional": false, "type": "struct" } }`)},
				Payload: record.Change{
					Before: nil,
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"after":  `{"_id": {"$oid": "63210f1a3bc50864fde46a84"},"name": "First Last","age": 205}`,
							"before": nil,
							"op":     "$unset",
							"source": map[string]interface{}{
								"opencdc.version": "v1",
							},
							"transaction": nil,
							"ts_ms":       float64(1674061777225),
						},
						"schema": map[string]interface{}{},
					},
				},
			},
			want: record.Record{
				Operation: record.OperationUpdate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
				},
				Payload: record.Change{
					After:  record.RawData{Raw: []byte(`{"_id": {"$oid": "63210f1a3bc50864fde46a84"},"name": "First Last","age": 205}`)},
					Before: nil,
				},
				Key: record.StructuredData{"id": `{ "$oid" : "63210f1a3bc50864fde46a84"}`},
			},
			wantErr: false,
		},
		{
			name: "mongoDB debezium record delete",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Key:      record.RawData{Raw: []byte(`{ "payload": { "id": "{ \"$oid\" : \"63210f1a3bc50864fde46a84\"}" }, "schema": { "fields": [ { "field": "id", "optional": false, "type": "string" } ], "name": "resource_7_735174.demo.user.Key", "optional": false, "type": "struct" } }`)},
				Payload: record.Change{
					Before: nil,
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"after":  nil,
							"before": nil,
							"op":     "d",
							"source": map[string]interface{}{
								"opencdc.version": "v1",
							},
							"transaction": nil,
							"ts_ms":       float64(1674061777225),
						},
						"schema": map[string]interface{}{},
					},
				},
			},
			want: record.Record{
				Operation: record.OperationDelete,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
				},
				Payload: record.Change{
					After:  nil,
					Before: nil,
				},
				Key: record.StructuredData{"id": `{ "$oid" : "63210f1a3bc50864fde46a84"}`},
			},
			wantErr: false,
		},
		{
			name: "mongoDB debezium record update",
			config: processor.Config{
				Settings: map[string]string{"format": "debezium"},
			},
			record: record.Record{
				Metadata: map[string]string{},
				Key:      record.RawData{Raw: []byte(`{ "payload": { "id": "{ \"$oid\" : \"63210f1a3bc50864fde46a84\"}" }, "schema": { "fields": [ { "field": "id", "optional": false, "type": "string" } ], "name": "resource_7_735174.demo.user.Key", "optional": false, "type": "struct" } }`)},
				Payload: record.Change{
					Before: nil,
					After: record.StructuredData{
						"payload": map[string]interface{}{
							"after":  nil,
							"before": nil,
							"op":     "u",
							"patch":  `{"$v": 2, "diff": { "d": { "age": false } } }`,
							"source": map[string]interface{}{
								"opencdc.version": "v1",
							},
							"transaction": nil,
							"ts_ms":       float64(1674061777225),
						},
						"schema": map[string]interface{}{},
					},
				},
			},
			want: record.Record{
				Operation: record.OperationUpdate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225000000",
					"opencdc.version": "v1",
				},
				Payload: record.Change{
					After: record.StructuredData{
						"patch": `{"$v": 2, "diff": { "d": { "age": false } } }`,
					},
					Before: nil,
				},
				Key: record.StructuredData{"id": `{ "$oid" : "63210f1a3bc50864fde46a84"}`},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			underTest, err := Unwrap(tt.config)
			is.NoErr(err)
			got, err := underTest.Process(context.Background(), tt.record)
			if (err != nil) != tt.wantErr {
				t.Fatalf("process() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}
