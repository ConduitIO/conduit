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

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestUnwrapKafkaConnect_Configure(t *testing.T) {
	testCases := []struct {
		name    string
		config  map[string]string
		wantErr string
	}{
		{
			name:    "optional not provided",
			config:  map[string]string{},
			wantErr: "",
		},
		{
			name:    "valid field (within .Payload)",
			config:  map[string]string{"field": ".Payload.After.something"},
			wantErr: "",
		},
		{
			name:    "invalid field",
			config:  map[string]string{"field": ".Key"},
			wantErr: `invalid configuration: error validating "field": ".Key" should match the regex "^.Payload": regex validation failed`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			err := newUnwrapKafkaConnect(log.Test(t)).Configure(context.Background(), tc.config)
			if tc.wantErr != "" {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			} else {
				is.NoErr(err)
			}
		})
	}
}
func TestUnwrapKafkaConnect_Process(t *testing.T) {
	testCases := []struct {
		name   string
		config map[string]string
		record opencdc.Record
		want   sdk.ProcessedRecord
	}{
		{
			name:   "structured payload kafka-connect",
			config: map[string]string{},
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
				Operation: opencdc.OperationSnapshot,
				Payload: opencdc.Change{
					After: opencdc.StructuredData{"description": "test2", "id": 27},
				},
				Key: opencdc.StructuredData{"id": 27},
			},
		},
		{
			name:   "raw payload not supported",
			config: map[string]string{},
			record: opencdc.Record{
				Metadata: map[string]string{},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(`{"description": "test2"}`),
				},
			},
			want: sdk.ErrorRecord{Error: cerrors.New("unexpected data type opencdc.RawData (only structured data is supported)")},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := newUnwrapKafkaConnect(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			is.NoErr(err)

			gotSlice := underTest.Process(context.Background(), []opencdc.Record{tc.record})
			is.Equal(1, len(gotSlice))
			AreEqual(t, tc.want, gotSlice[0])
		})
	}
}
