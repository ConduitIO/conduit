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
	"maps"
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

func TestEncodeProcessor_Process_StructuredData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	cfg := config.Config{
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
	err := underTest.Configure(ctx, cfg)
	is.NoErr(err)

	// skipping Open(), so we can inject a mock encoder
	mockEncoder := NewMockEncoder(gomock.NewController(t))
	mockEncoder.EXPECT().
		Encode(ctx, input.Payload.After).
		Return(want.Payload.After.(opencdc.RawData), nil)
	underTest.encoder = mockEncoder

	got := underTest.Process(ctx, []opencdc.Record{input})
	is.Equal(1, len(got))
	is.Equal("", cmp.Diff(want, got[0], internal.CmpProcessedRecordOpts...))
}

func TestEncodeProcessor_Process_RawData(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	cfg := config.Config{
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
	err := underTest.Configure(ctx, cfg)
	is.NoErr(err)

	// skipping Open(), so we can inject a mock encoder
	mockEncoder := NewMockEncoder(gomock.NewController(t))
	mockEncoder.EXPECT().
		Encode(ctx, opencdc.StructuredData{"field_int": float64(123)}).
		Return(want.Payload.After.(opencdc.RawData), nil)
	underTest.encoder = mockEncoder

	got := underTest.Process(ctx, []opencdc.Record{input})
	is.Equal(1, len(got))
	is.Equal("", cmp.Diff(want, got[0], internal.CmpProcessedRecordOpts...))
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

			cfg := config.Config{
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
			err := underTest.Configure(ctx, cfg)
			is.NoErr(err)

			// skipping Open(), so we can inject a mock encoder
			mockEncoder := NewMockEncoder(gomock.NewController(t))
			mockEncoder.EXPECT().
				Encode(ctx, opencdc.StructuredData{"field_int": float64(123)}).
				Return(encodedValue, nil)
			underTest.encoder = mockEncoder

			got := underTest.Process(ctx, []opencdc.Record{input})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}

func TestEncodeProcessor_Process_EmptyPayloadField(t *testing.T) {
	testCases := []struct {
		name              string
		field             string
		input             opencdc.Record
		mockEncoder       func(ctx context.Context) *MockEncoder
		wantPayloadBefore opencdc.Data
		wantPayloadAfter  opencdc.Data
	}{
		{
			name:  "empty payload.Before encoding",
			field: ".Payload.Before",
			input: opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{},
					After: opencdc.StructuredData{
						"something": opencdc.RawData(`{"field_int": 123}`),
					},
				},
			},
			wantPayloadBefore: opencdc.RawData(`{}`),
			wantPayloadAfter: opencdc.StructuredData{
				"something": opencdc.RawData(`{"field_int": 123}`),
			},
			mockEncoder: func(ctx context.Context) *MockEncoder {
				m := NewMockEncoder(gomock.NewController(t))
				m.EXPECT().
					Encode(ctx, opencdc.StructuredData{}).
					Return(opencdc.RawData(`{}`), nil)
				return m
			},
		},
		{
			name:  "empty payload.After encoding",
			field: ".Payload.After",
			input: opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"something": opencdc.RawData(`{"field_int": 123}`),
					},
					After: opencdc.StructuredData{},
				},
			},
			wantPayloadBefore: opencdc.StructuredData{
				"something": opencdc.RawData(`{"field_int": 123}`),
			},
			wantPayloadAfter: opencdc.RawData(`{}`),
			mockEncoder: func(ctx context.Context) *MockEncoder {
				m := NewMockEncoder(gomock.NewController(t))
				m.EXPECT().
					Encode(ctx, opencdc.StructuredData{}).
					Return(opencdc.RawData(`{}`), nil)
				return m
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			cfg := config.Config{
				"field":                       tc.field,
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
			}

			want := sdk.SingleRecord(tc.input.Clone())
			want.Payload.After = tc.wantPayloadAfter
			want.Payload.Before = tc.wantPayloadBefore

			underTest := NewEncodeProcessor(log.Nop())
			err := underTest.Configure(ctx, cfg)
			is.NoErr(err)

			// skipping Open(), so we can inject a mock encoder
			mockEncoder := tc.mockEncoder(ctx)
			underTest.encoder = mockEncoder

			got := underTest.Process(ctx, []opencdc.Record{tc.input})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}

// Invariant 6: schema metadata that described a field must not outlive the
// field being replaced by Avro bytes, or a destination decodes it again.
func TestEncodeProcessor_Process_SchemaMetadata(t *testing.T) {
	allSchemaKeys := func() opencdc.Metadata {
		return opencdc.Metadata{
			"keep":                               "me",
			opencdc.MetadataPayloadSchemaSubject: "p",
			opencdc.MetadataPayloadSchemaVersion: "2",
			opencdc.MetadataKeySchemaSubject:     "k",
			opencdc.MetadataKeySchemaVersion:     "3",
		}
	}
	testCases := []struct {
		name     string
		field    string
		input    opencdc.Record
		wantGone []string
	}{
		{
			name:  "payload after",
			field: ".Payload.After",
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}},
			wantGone: []string{
				opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion,
			},
		},
		{
			name:  "payload after, bracket spelling",
			field: `.Payload["After"]`,
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}},
			wantGone: []string{
				opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion,
			},
		},
		{
			name:  "payload after, lowercase spelling",
			field: ".Payload.after",
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}},
			wantGone: []string{
				opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion,
			},
		},
		{
			name:  "payload before, bracket spelling",
			field: `.Payload["Before"]`,
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{Before: opencdc.StructuredData{"a": 1}}},
			wantGone: []string{
				opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion,
			},
		},
		{
			name:  "key, lowercase spelling",
			field: ".key",
			input: opencdc.Record{Metadata: allSchemaKeys(), Key: opencdc.StructuredData{"a": 1}},
			wantGone: []string{
				opencdc.MetadataKeySchemaSubject, opencdc.MetadataKeySchemaVersion,
			},
		},
		{
			name:  "payload before",
			field: ".Payload.Before",
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{Before: opencdc.StructuredData{"a": 1}}},
			wantGone: []string{
				opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion,
			},
		},
		{
			name:  "key",
			field: ".Key",
			input: opencdc.Record{Metadata: allSchemaKeys(), Key: opencdc.StructuredData{"a": 1}},
			wantGone: []string{
				opencdc.MetadataKeySchemaSubject, opencdc.MetadataKeySchemaVersion,
			},
		},
		{
			// the record stays structured; the SDK schema middleware owns its schema
			name:  "nested field keeps schema metadata",
			field: ".Payload.After.nested",
			input: opencdc.Record{Metadata: allSchemaKeys(), Payload: opencdc.Change{After: opencdc.StructuredData{"nested": `{"a": 1}`}}},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := NewEncodeProcessor(log.Nop())
			is.NoErr(underTest.Configure(ctx, config.Config{
				"field":                       tc.field,
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "testsubject",
			}))
			mockEncoder := NewMockEncoder(gomock.NewController(t))
			mockEncoder.EXPECT().Encode(ctx, gomock.Any()).Return(opencdc.RawData("encoded"), nil)
			underTest.encoder = mockEncoder

			before := maps.Clone(tc.input.Metadata)
			got := underTest.Process(ctx, []opencdc.Record{tc.input})
			is.Equal(1, len(got))
			out, ok := got[0].(sdk.SingleRecord)
			is.True(ok)

			want := maps.Clone(before)
			for _, k := range tc.wantGone {
				delete(want, k)
			}
			is.Equal("", cmp.Diff(map[string]string(want), map[string]string(out.Metadata)))
			is.Equal(out.Metadata["keep"], "me")
			// the input record's metadata map is shared with the caller: untouched
			is.Equal("", cmp.Diff(map[string]string(before), map[string]string(tc.input.Metadata)))
		})
	}
}
