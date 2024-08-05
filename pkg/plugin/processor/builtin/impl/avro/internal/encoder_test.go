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

package internal

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestEncodeDecode_ExtractAndUploadSchemaStrategy(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	client, err := schemaregistrytest.TestSchemaRegistry()
	is.NoErr(err)

	have := opencdc.StructuredData{
		"myString": "bar",
		"myInt":    1,
		"myFloat":  2.3,
		"myMap": map[string]any{
			"foo": true,
			"bar": 2.2,
		},
		"myStruct": opencdc.StructuredData{
			"foo": 1,
			"bar": false,
		},
		"mySlice": []int{1, 2, 3},
	}
	want := opencdc.StructuredData{
		"myString": "bar",
		"myInt":    int64(1),
		"myFloat":  2.3,
		"myMap": map[string]any{
			"foo": true,
			"bar": 2.2,
		},
		"myStruct": map[string]any{ // records are unmarshalled into a map
			"foo": int64(1),
			"bar": false,
		},
		"mySlice": []any{int64(1), int64(2), int64(3)}, // slice without type
	}

	enc := NewEncoder(client, logger, ExtractAndUploadSchemaStrategy{
		Subject: "test1",
	})
	dec := NewDecoder(client, logger)

	bytes, err := enc.Encode(ctx, have)
	is.NoErr(err)

	got, err := dec.Decode(ctx, bytes)
	is.NoErr(err)

	is.Equal(want, got)
}

func TestEncodeDecode_DownloadStrategy_Avro(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	client, err := schemaregistry.NewClient(logger, sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)

	have := opencdc.StructuredData{
		"myString": "bar",
		"myInt":    1,
	}
	want := opencdc.StructuredData{
		"myString": "bar",
		"myInt":    1,
	}
	ss, err := client.CreateSchema(ctx, "test2", sr.Schema{
		Type: sr.TypeAvro,
		Schema: `
{
  "type":"record",
  "name":"record",
  "fields":[
    {"name":"myString","type":"string"},
    {"name":"myInt","type":"int"}
  ]
}`,
	})
	is.NoErr(err)

	enc := NewEncoder(client, logger, DownloadSchemaStrategy{
		Subject: ss.Subject,
		Version: ss.Version,
	})
	dec := NewDecoder(client, logger)

	bytes, err := enc.Encode(ctx, have)
	is.NoErr(err)

	got, err := dec.Decode(ctx, bytes)
	is.NoErr(err)

	is.Equal(want, got)
}
