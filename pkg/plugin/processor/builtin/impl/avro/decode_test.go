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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDecodeProcessor_Process_RawData_CustomField(t *testing.T) {
	data := `{"field_int": 123}`
	testCases := []struct {
		name  string
		field interface{}
	}{
		{
			name:  "opencdc.RawData",
			field: opencdc.RawData(data),
		},
		{
			name:  "[]byte",
			field: []byte(data),
		},
		{
			name:  "string (base64 encoded byte slice",
			field: data,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			cfg := config.Config{
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
			err := underTest.Configure(ctx, cfg)
			is.NoErr(err)

			// skipping Open(), so we can inject a mock encoder
			mockDecoder := NewMockDecoder(gomock.NewController(t))
			mockDecoder.EXPECT().
				Decode(ctx, opencdc.RawData(data)).
				Return(decodedVal, nil)
			underTest.(*decodeProcessor).decoder = mockDecoder

			got := underTest.Process(ctx, []opencdc.Record{input})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}
