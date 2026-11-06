// Copyright © 2026 Meroxa, Inc.
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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	avrointernal "github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/internal"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestEncodeProcessor_FromRecord_PassesCarriedSchema checks which metadata the
// processor reads as the carried schema for each field.
func TestEncodeProcessor_FromRecord_PassesCarriedSchema(t *testing.T) {
	md := opencdc.Metadata{
		opencdc.MetadataKeySchemaSubject:     "key-subject",
		opencdc.MetadataKeySchemaVersion:     "3",
		opencdc.MetadataPayloadSchemaSubject: "payload-subject",
		opencdc.MetadataPayloadSchemaVersion: "7",
	}
	testCases := []struct {
		field string
		md    opencdc.Metadata
		want  avrointernal.SchemaRef
	}{
		{field: ".Payload.After", md: md, want: avrointernal.SchemaRef{Subject: "payload-subject", Version: 7}},
		{field: ".Payload.Before", md: md, want: avrointernal.SchemaRef{Subject: "payload-subject", Version: 7}},
		{field: ".Key", md: md, want: avrointernal.SchemaRef{Subject: "key-subject", Version: 3}},
		{field: ".Payload.After", md: opencdc.Metadata{}, want: avrointernal.SchemaRef{}},
	}
	for _, tc := range testCases {
		t.Run(tc.field, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			p := NewEncodeProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, config.Config{
				"field":                     tc.field,
				"schema.strategy":           "fromRecord",
				"schema.fromRecord.subject": "out",
			}))
			m := NewMockEncoder(gomock.NewController(t))
			m.EXPECT().Encode(ctx, gomock.Any(), tc.want).Return(opencdc.RawData("encoded"), nil)
			p.encoder = m

			data := opencdc.StructuredData{"a": 1}
			rec := opencdc.Record{Metadata: tc.md, Key: data, Payload: opencdc.Change{Before: data, After: data}}
			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			_, ok := out[0].(sdk.SingleRecord)
			is.True(ok)
		})
	}
}

// TestEncodeProcessor_FromRecord_InvalidSchemaMetadata: half-set or
// malformed schema metadata is an error, never "no schema" (which would fall
// back to inference) and never a guess.
func TestEncodeProcessor_FromRecord_InvalidSchemaMetadata(t *testing.T) {
	testCases := map[string]opencdc.Metadata{
		"subject without version": {opencdc.MetadataPayloadSchemaSubject: "s"},
		"version without subject": {opencdc.MetadataPayloadSchemaVersion: "1"},
		"version not a number":    {opencdc.MetadataPayloadSchemaSubject: "s", opencdc.MetadataPayloadSchemaVersion: "x"},
		"version zero":            {opencdc.MetadataPayloadSchemaSubject: "s", opencdc.MetadataPayloadSchemaVersion: "0"},
	}
	for name, md := range testCases {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			p := NewEncodeProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, config.Config{
				"schema.strategy":           "fromRecord",
				"schema.fromRecord.subject": "out",
			}))
			p.encoder = NewMockEncoder(gomock.NewController(t)) // must not be called

			rec := opencdc.Record{Metadata: md, Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}}
			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			er, ok := out[0].(sdk.ErrorRecord)
			is.True(ok)
			ce, ok := conduiterr.Get(er.Error)
			is.True(ok)
			is.Equal(ce.Code, avrointernal.CodeSchemaUnsupported)
		})
	}
}

// TestEncodeProcessor_OtherStrategiesIgnoreSchemaMetadata: the existing
// strategies do not read the schema metadata, so malformed metadata does not
// change their behavior.
func TestEncodeProcessor_OtherStrategiesIgnoreSchemaMetadata(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	p := NewEncodeProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{
		"schema.strategy":             "autoRegister",
		"schema.autoRegister.subject": "out",
	}))
	m := NewMockEncoder(gomock.NewController(t))
	m.EXPECT().Encode(ctx, gomock.Any(), avrointernal.SchemaRef{}).Return(opencdc.RawData("encoded"), nil)
	p.encoder = m

	rec := opencdc.Record{
		Metadata: opencdc.Metadata{opencdc.MetadataPayloadSchemaSubject: "s", opencdc.MetadataPayloadSchemaVersion: "x"},
		Payload:  opencdc.Change{After: opencdc.StructuredData{"a": 1}},
	}
	out := p.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(out), 1)
	_, ok := out[0].(sdk.SingleRecord)
	is.True(ok)
}

func TestEncodeProcessor_FromRecord_NestedFieldIsAConfigError(t *testing.T) {
	is := is.New(t)
	p := NewEncodeProcessor(log.Nop())
	err := p.Configure(context.Background(), config.Config{
		"field":                     ".Payload.After.nested",
		"schema.strategy":           "fromRecord",
		"schema.fromRecord.subject": "out",
	})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
	is.Equal(ce.ConfigPath, "field")
}
