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
	"github.com/google/go-cmp/cmp"
	"testing"

	"github.com/matryer/is"
)

func TestPosition_Clone(t *testing.T) {
	is := is.New(t)

	p := Position("position 123")
	is.Equal(p, p.Clone())
}

func TestRawData_Clone(t *testing.T) {
	is := is.New(t)

	original := RawData{Raw: []byte("position 123")}
	clone, ok := original.Clone().(RawData)

	is.True(ok)
	is.Equal(original, clone)
}

func TestStructuredData_Clone(t *testing.T) {
	is := is.New(t)

	original := StructuredData{
		"name":  "conduit",
		"tags":  []string{"go", "data-streaming"},
		"stars": 123456,
	}
	clone, ok := original.Clone().(StructuredData)

	is.True(ok) // expected StructuredData
	is.Equal(original, clone)
}

func TestMetadata_Clone(t *testing.T) {
	is := is.New(t)

	original := Metadata{"created-by": "someone"}
	is.Equal(original, original.Clone())
}

func TestRecord_Clone(t *testing.T) {
	testCases := []struct {
		name  string
		input Record
	}{
		{
			name:  "zero record",
			input: Record{},
		},
		{
			name: "full record",
			input: Record{
				Position:  Position("standing"),
				Operation: OperationUpdate,
				Metadata:  Metadata{"foo": "bar"},
				Key:       RawData{Raw: []byte("padlock-key")},
				Payload: Change{
					Before: RawData{Raw: []byte("yellow")},
					After:  StructuredData{"colour": "orange"},
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !cmp.Equal(tc.input, tc.input.Clone()) {
				t.Logf("diff: %v\n", cmp.Diff(tc.input, tc.input.Clone()))
				t.Fail() // clone not equal to original
			}
		})
	}
}

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
	want := map[string]interface{}{
		"position":  []byte("foo"),
		"operation": "create",
		"metadata": map[string]interface{}{
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
	}
	is.Equal(want, got)
}
