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

package avro

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDecodeProcessor_Process_RawData_CustomField(t *testing.T) {
	testCases := []struct {
		name  string
		field interface{}
	}{
		{
			name:  "opencdc.RawData",
			field: opencdc.RawData(`{"field_int": 123}`),
		},
		{
			name:  "[]byte",
			field: []byte(`{"field_int": 123}`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			config := map[string]string{
				"url":   "http://localhost",
				"field": ".Payload.Before.something",
			}
			input := opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"something": tc.field,
					},
					After: opencdc.RawData("after data"),
				},
			}

			decodedVal := opencdc.StructuredData{"decoded": "value"}
			want := sdk.SingleRecord(input.Clone())
			want.Payload.Before.(opencdc.StructuredData)["something"] = decodedVal

			underTest := NewDecodeProcessor(log.Nop())
			err := underTest.Configure(ctx, config)
			is.NoErr(err)

			// skipping Open(), so we can inject a mock encoder
			mockDecoder := NewMockDecoder(gomock.NewController(t))
			mockDecoder.EXPECT().
				Decode(ctx, opencdc.RawData(`{"field_int": 123}`)).
				Return(decodedVal, nil)
			underTest.(*decodeProcessor).decoder = mockDecoder

			got := underTest.Process(ctx, []opencdc.Record{input})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(want, got[0], cmpProcessedRecordOpts...))
		})
	}
}
