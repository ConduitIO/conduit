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
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"testing"
)

func TestEncodeProcessor_Process_StructuredData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	config := map[string]string{
		"url":                         "http://localhost",
		"schema.strategy":             "autoRegister",
		"schema.autoRegister.subject": "testsubject",
	}
	input := opencdc.Record{
		Position:  opencdc.Position("test position"),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.RawData("test key"),
		Payload: opencdc.Change{
			After: opencdc.StructuredData{
				"field_int": 123,
			},
		},
	}
	want := sdk.SingleRecord(input.Clone())
	want.Payload.After = opencdc.RawData("encoded")

	underTest := NewEncodeProcessor(log.Nop())
	err := underTest.Configure(ctx, config)
	is.NoErr(err)

	// skipping Open(), so we can inject a mock encoder
	mockEncoder := NewMockEncoder(gomock.NewController(t))
	mockEncoder.EXPECT().
		Encode(ctx, input.Payload.After).
		Return(want.Payload.After, nil)
	underTest.(*encodeProcessor).encoder = mockEncoder

	got := underTest.Process(ctx, []opencdc.Record{input})
	is.Equal(1, len(got))
	is.Equal("", cmp.Diff(want, got[0], cmpProcessedRecordOpts...))
}

func TestEncodeProcessor_Process_RawData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	config := map[string]string{
		"url":                         "http://localhost",
		"schema.strategy":             "autoRegister",
		"schema.autoRegister.subject": "testsubject",
	}
	input := opencdc.Record{
		Position:  opencdc.Position("test position"),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.RawData("test key"),
		Payload: opencdc.Change{
			After: opencdc.RawData(`{"field_int": 123}`),
		},
	}
	want := sdk.SingleRecord(input.Clone())
	want.Payload.After = opencdc.RawData("encoded")

	underTest := NewEncodeProcessor(log.Nop())
	err := underTest.Configure(ctx, config)
	is.NoErr(err)

	// skipping Open(), so we can inject a mock encoder
	mockEncoder := NewMockEncoder(gomock.NewController(t))
	mockEncoder.EXPECT().
		Encode(ctx, opencdc.StructuredData{"field_int": float64(123)}).
		Return(want.Payload.After, nil)
	underTest.(*encodeProcessor).encoder = mockEncoder

	got := underTest.Process(ctx, []opencdc.Record{input})
	is.Equal(1, len(got))
	is.Equal("", cmp.Diff(want, got[0], cmpProcessedRecordOpts...))
}

func TestEncodeProcessor_Process_RawData_CustomField(t *testing.T) {
	testCases := []struct {
		name  string
		field interface{}
	}{
		{
			name:  "opencdc.RawData",
			field: opencdc.RawData(`{"field_int": 123}`),
		},
		{
			name:  "string",
			field: `{"field_int": 123}`,
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
				"url":                         "http://localhost",
				"field":                       ".Payload.Before.something",
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
			}
			input := opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"something": tc.field,
					},
					After: opencdc.RawData("after data"),
				},
			}

			encodedValue := opencdc.RawData("encoded")
			want := sdk.SingleRecord(input.Clone())
			want.Payload.Before.(opencdc.StructuredData)["something"] = encodedValue

			underTest := NewEncodeProcessor(log.Nop())
			err := underTest.Configure(ctx, config)
			is.NoErr(err)

			// skipping Open(), so we can inject a mock encoder
			mockEncoder := NewMockEncoder(gomock.NewController(t))
			mockEncoder.EXPECT().
				Encode(ctx, opencdc.StructuredData{"field_int": float64(123)}).
				Return(encodedValue, nil)
			underTest.(*encodeProcessor).encoder = mockEncoder

			got := underTest.Process(ctx, []opencdc.Record{input})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(want, got[0], cmpProcessedRecordOpts...))
		})
	}
}
