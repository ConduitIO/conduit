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

type testCase[C, O any] struct {
	name        string
	conduitType C
	opencdcType O
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
	testCases := positionTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Position: tc.conduitType}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Position)
		})
	}
}

func TestRecord_ToOpenCDC_Metadata(t *testing.T) {
	testCases := metadataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Metadata: tc.conduitType}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Metadata)
		})
	}
}

func TestRecord_ToOpenCDC_Operation(t *testing.T) {
	testCases := operationTestCases()

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v", tc.conduitType), func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Operation: tc.conduitType}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Operation)
		})
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
	testCases := positionTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{
				Position: tc.opencdcType,
			})
			is.Equal(tc.conduitType, got.Position)
		})
	}
}

func TestRecord_FromOpenCDC_Metadata(t *testing.T) {
	testCases := metadataTestCases()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			got := FromOpenCDC(opencdc.Record{Metadata: tc.opencdcType})
			is.Equal(tc.conduitType, got.Metadata)
		})
	}
}

func TestRecord_FromOpenCDC_Operation(t *testing.T) {
	testCases := operationTestCases()

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v", tc.conduitType), func(t *testing.T) {
			is := is.New(t)

			underTest := Record{Operation: tc.conduitType}
			got := underTest.ToOpenCDC()
			is.Equal(tc.opencdcType, got.Operation)
		})
	}
}

func positionTestCases() []testCase[Position, opencdc.Position] {
	return []testCase[Position, opencdc.Position]{
		{
			name: "nil",
		},
		{
			name:        "raw",
			opencdcType: opencdc.Position("raw, uncooked data"),
			conduitType: Position("raw, uncooked data"),
		},
	}
}

func dataTestCases() []testCase[Data, opencdc.Data] {
	return []testCase[Data, opencdc.Data]{
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

func operationTestCases() []testCase[Operation, opencdc.Operation] {
	return []testCase[Operation, opencdc.Operation]{
		{
			conduitType: OperationCreate,
			opencdcType: opencdc.OperationCreate,
		},
		{
			conduitType: OperationSnapshot,
			opencdcType: opencdc.OperationSnapshot,
		},
		{
			conduitType: OperationUpdate,
			opencdcType: opencdc.OperationUpdate,
		},
		{
			conduitType: OperationDelete,
			opencdcType: opencdc.OperationDelete,
		},
	}
}

func metadataTestCases() []testCase[Metadata, opencdc.Metadata] {
	return []testCase[Metadata, opencdc.Metadata]{
		{
			name: "nil",
		},
		{
			name:        "empty",
			conduitType: Metadata{},
			opencdcType: opencdc.Metadata{},
		},
		{
			name: "non-empty",
			conduitType: Metadata{
				"k":                             "v",
				MetadataOpenCDCVersion:          OpenCDCVersion,
				MetadataConduitSourcePluginName: "file",
			},
			opencdcType: opencdc.Metadata{
				"k":                                     "v",
				opencdc.MetadataOpenCDCVersion:          opencdc.OpenCDCVersion,
				opencdc.MetadataConduitSourcePluginName: "file",
			},
		},
	}
}
