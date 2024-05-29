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

package schemaregistry

import (
	"context"
	"github.com/conduitio/conduit/pkg/schema"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"github.com/matryer/is"
)

func TestEncodeDecode_ExtractAndUploadSchemaStrategy(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	var serde sr.Serde
	client, err := NewClient(logger, sr.URLs(schema.TestSchemaRegistryURL(t)))
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
		"myInt":    1,
		"myFloat":  2.3,
		"myMap": map[string]any{
			"foo": true,
			"bar": 2.2,
		},
		"myStruct": map[string]any{ // records are unmarshaled into a map
			"foo": 1,
			"bar": false,
		},
		"mySlice": []any{1, 2, 3}, // slice without type
	}

	for schemaType := range DefaultSchemaFactories {
		t.Run(schemaType.String(), func(t *testing.T) {
			is := is.New(t)
			enc := NewEncoder(client, logger, &serde, ExtractAndUploadSchemaStrategy{
				Type:    schemaType,
				Subject: "test1" + schemaType.String(),
			})
			dec := NewDecoder(client, logger, &serde)

			bytes, err := enc.Encode(ctx, have)
			is.NoErr(err)

			got, err := dec.Decode(ctx, bytes)
			is.NoErr(err)

			is.Equal(want, got)
		})
	}
}

func TestEncodeDecode_DownloadStrategy_Avro(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	var serde sr.Serde
	client, err := NewClient(logger, sr.URLs(schema.TestSchemaRegistryURL(t)))
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

	enc := NewEncoder(client, logger, &serde, DownloadSchemaStrategy{
		Subject: ss.Subject,
		Version: ss.Version,
	})
	dec := NewDecoder(client, logger, &serde)

	bytes, err := enc.Encode(ctx, have)
	is.NoErr(err)

	got, err := dec.Decode(ctx, bytes)
	is.NoErr(err)

	is.Equal(want, got)
}
