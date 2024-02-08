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
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestRecord_Clone(t *testing.T) {
	type user struct {
		Name string
	}

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
					After: StructuredData{
						"bool": true,

						"int":   1,
						"int8":  int8(1),
						"int16": int16(1),
						"int32": int32(1),
						"int64": int64(1),

						"float32": float32(1.2),
						"float64": 1.2,

						"string": "orange",

						"string-slice": []string{"a"},
						"map":          map[string]string{"a": "A", "b": "B"},

						"user": user{Name: "john"},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.input.Clone()
			if !cmp.Equal(tc.input, got) {
				t.Logf("diff: %v\n", cmp.Diff(tc.input, got))
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

func TestRecord_ToOpenCDC_Keys(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Key: tc.in}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Key)
		})
	}
}

func TestRecord_ToOpenCDC_PayloadBefore(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{
				Payload: Change{Before: tc.in},
			}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Payload.Before)
		})
	}
}

func TestRecord_ToOpenCDC_PayloadAfter(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{
				Payload: Change{After: tc.in},
			}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Payload.After)
		})
	}
}

func TestRecord_ToOpenCDC_Position(t *testing.T) {
	testCases := []struct {
		name string
		in   Position
		want opencdc.Position
	}{
		{
			name: "nil",
		},
		{
			name: "raw",
			in:   Position("raw, uncooked data"),
			want: opencdc.Position("raw, uncooked data"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Position: tc.in}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Position)
		})
	}
}

func TestRecord_ToOpenCDC_Metadata(t *testing.T) {
	testCases := []struct {
		name string
		in   Metadata
		want opencdc.Metadata
	}{
		{
			name: "nil",
		},
		{
			name: "empty",
			in:   Metadata{},
			want: opencdc.Metadata{},
		},
		{
			name: "non-empty",
			in: Metadata{
				"k":                             "v",
				MetadataOpenCDCVersion:          OpenCDCVersion,
				MetadataConduitSourcePluginName: "file",
			},
			want: opencdc.Metadata{
				"k":                                     "v",
				opencdc.MetadataOpenCDCVersion:          opencdc.OpenCDCVersion,
				opencdc.MetadataConduitSourcePluginName: "file",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Metadata: tc.in}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Metadata)
		})
	}
}

func TestRecord_ToOpenCDC_Operation(t *testing.T) {
	testCases := []struct {
		in   Operation
		want opencdc.Operation
	}{
		{
			in:   OperationCreate,
			want: opencdc.OperationCreate,
		},
		{
			in:   OperationSnapshot,
			want: opencdc.OperationSnapshot,
		},
		{
			in:   OperationUpdate,
			want: opencdc.OperationUpdate,
		},
		{
			in:   OperationDelete,
			want: opencdc.OperationDelete,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v", tc.in), func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Operation: tc.in}
			got := underTest.ToOpenCDC()
			is.Equal(tc.want, got.Operation)
		})
	}
}

type dataTestCase struct {
	name string
	in   Data
	want opencdc.Data
}

func dataTestCases() []dataTestCase {
	return []dataTestCase{
		{
			name: "nil",
			in:   nil,
			want: nil,
		},
		{
			name: "raw",
			in:   RawData{Raw: []byte("raw, uncooked data")},
			want: opencdc.RawData("raw, uncooked data"),
		},
		{
			name: "structured",
			in: StructuredData{
				"key1": "string-value",
				"key2": 123,
				"key3": []int{4, 5, 6},
				"key4": map[string]interface{}{
					"letters": "abc",
				},
			},
			want: opencdc.StructuredData{
				"key1": "string-value",
				"key2": 123,
				"key3": []int{4, 5, 6},
				"key4": map[string]interface{}{
					"letters": "abc",
				},
			},
		},
	}
}
