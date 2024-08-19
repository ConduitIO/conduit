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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestKafkaConnectProcessor_Configure(t *testing.T) {
	testCases := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name:    "optional parameter not provided",
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

			err := NewKafkaConnectProcessor(log.Test(t)).Configure(context.Background(), tc.config)
			if tc.wantErr != "" {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			} else {
				is.NoErr(err)
			}
		})
	}
}

func TestKafkaConnectProcessor_Process(t *testing.T) {
	testCases := []struct {
		name   string
		config config.Config
		record opencdc.Record
		want   sdk.ProcessedRecord
	}{
		{
			name:   "structured payload",
			config: config.Config{},
			record: opencdc.Record{
				Metadata: map[string]string{},
				Payload: opencdc.Change{
					Before: opencdc.StructuredData(nil),
					After: opencdc.StructuredData{
						"payload": map[string]any{
							"description": "test2",
							"id":          27,
						},
						"schema": map[string]any{},
					},
				},
				Key: opencdc.StructuredData{
					"payload": map[string]any{
						"id": 27,
					},
					"schema": map[string]any{},
				},
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationCreate,
				Metadata:  map[string]string{},
				Payload: opencdc.Change{
					After: opencdc.StructuredData{"description": "test2", "id": 27},
				},
				Key: opencdc.StructuredData{"id": 27},
			},
		},
		{
			name:   "raw payload",
			config: config.Config{},
			record: opencdc.Record{
				Position:  opencdc.Position("test position"),
				Operation: opencdc.OperationSnapshot,
				Metadata: map[string]string{
					"metadata-key": "metadata-value",
				},
				Key: opencdc.RawData("key"),
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.RawData(`{
						"payload": {
							"description": "test2"
						},
						"schema": {}
					}`),
				},
			},
			want: sdk.SingleRecord{
				Position:  opencdc.Position("test position"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"metadata-key": "metadata-value",
				},
				Key: opencdc.RawData("key"),
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"description": "test2",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := NewKafkaConnectProcessor(log.Test(t))
			err := underTest.Configure(ctx, tc.config)
			is.NoErr(err)

			got := underTest.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(tc.want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}
