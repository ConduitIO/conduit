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

package builtin

import (
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"testing"
)

func TestUnwrapDebezium_Process(t *testing.T) {
	testCases := []struct {
		name    string
		record  opencdc.Record
		want    opencdc.Record
		config  map[string]string
		wantErr bool
	}{
		{
			name:   "raw payload",
			config: map[string]string{"format": "debezium"},
			record: opencdc.Record{
				Metadata: map[string]string{},
				Key:      opencdc.RawData(`{"payload":"id"}`),
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
			want: opencdc.Record{
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"opencdc.readAt":  "1674061777225877000",
					"opencdc.version": "v1",
				},
				Key:      opencdc.RawData("id"),
				Position: []byte("position"),
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.StructuredData{"description": "test1", "id": float64(27)},
				},
			},
			wantErr: false,
		},
		{
			name:   "structured payload",
			config: map[string]string{"format": "debezium"},
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
			want: opencdc.Record{
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
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := newUnwrapDebezium(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			is.NoErr(err)

			got := underTest.Process(context.Background(), []opencdc.Record{tc.record})

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}
