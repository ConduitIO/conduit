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
	"github.com/matryer/is"
)

type dataTestCase struct {
	name        string
	conduitType Data
	opencdcType opencdc.Data
}

func TestRecord_ToOpenCDC_Keys(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Key: tc.conduitType}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Key)
		})
	}
}

func TestRecord_ToOpenCDC_PayloadBefore(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{
				Payload: Change{Before: tc.conduitType},
			}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Payload.Before)
		})
	}
}

func TestRecord_ToOpenCDC_PayloadAfter(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{
				Payload: Change{After: tc.conduitType},
			}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Payload.After)
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

func dataTestCases() []dataTestCase {
	return []dataTestCase{
		{
			name:        "nil",
			conduitType: nil,
			opencdcType: nil,
		},
		{
			name:        "raw",
			conduitType: RawData{Raw: []byte("raw, uncooked data")},
			opencdcType: opencdc.RawData("raw, uncooked data"),
		},
		{
			name: "structured",
			conduitType: StructuredData{
				"key1": "string-value",
				"key2": 123,
				"key3": []int{4, 5, 6},
				"key4": map[string]interface{}{
					"letters": "abc",
				},
			},
			opencdcType: opencdc.StructuredData{
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

func TestRecord_FromOpenCDC_Keys(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{Key: tc.opencdcType})
			is.Equal(tc.conduitType, got.Key)
		})
	}
}

func TestRecord_FromOpenCDC_PayloadBefore(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{
				Payload: opencdc.Change{
					Before: tc.opencdcType,
				},
			})
			is.Equal(tc.conduitType, got.Payload.Before)
		})
	}
}

func TestRecord_FromOpenCDC_PayloadAfter(t *testing.T) {
	testCases := dataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{
				Payload: opencdc.Change{
					After: tc.opencdcType,
				},
			})
			is.Equal(tc.conduitType, got.Payload.After)
		})
	}
}

func TestRecord_FromOpenCDC_Position(t *testing.T) {
	testCases := []struct {
		name string
		want Position
		in   opencdc.Position
	}{
		{
			name: "nil",
		},
		{
			name: "raw",
			in:   opencdc.Position("raw, uncooked data"),
			want: Position("raw, uncooked data"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{
				Position: tc.in,
			})
			is.Equal(tc.want, got.Position)
		})
	}
}
