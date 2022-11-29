// Copyright © 2022 Meroxa, Inc.
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

package record

import (
	"testing"

	"github.com/matryer/is"
)

func TestRecord_Bytes(t *testing.T) {
	is := is.New(t)

	r := Record{
		Position:  Position("foo"),
		Operation: OperationCreate,
		Metadata: Metadata{
			MetadataConduitSourcePluginName: "example",
		},
		Key: RawData{Raw: []byte("bar")},
		Payload: Change{
			Before: nil,
			After: StructuredData{
				"foo": "bar",
				"baz": "qux",
			},
		},
	}

	want := `{"position":"Zm9v","operation":"create","metadata":{"conduit.source.plugin.name":"example","opencdc.version":"v1"},"key":"YmFy","payload":{"before":null,"after":{"baz":"qux","foo":"bar"}}}`

	got := string(r.Bytes())
	is.Equal(got, want)

	is.Equal(r.Metadata, Metadata{MetadataConduitSourcePluginName: "example"}) // expected metadata to stay unaltered
}

func TestRecord_ToMap(t *testing.T) {
	is := is.New(t)

	r := Record{
		Position:  Position("foo"),
		Operation: OperationCreate,
		Metadata: Metadata{
			MetadataConduitSourcePluginName: "example",
		},
		Key: RawData{Raw: []byte("bar")},
		Payload: Change{
			Before: nil,
			After: StructuredData{
				"foo": "bar",
				"baz": "qux",
			},
		},
	}

	got := r.Map()
	is.Equal(map[string]any{
		"position":  []byte("foo"),
		"operation": "create",
		"metadata": map[string]string{
			MetadataConduitSourcePluginName: "example",
		},
		"key": []byte("bar"),
		"payload": map[string]interface{}{
			"before": nil,
			"after": map[string]interface{}{
				"foo": "bar",
				"baz": "qux",
			},
		},
	}, got)
}
