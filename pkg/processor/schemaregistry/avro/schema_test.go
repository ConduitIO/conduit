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

package avro

import (
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hamba/avro/v2"
	"github.com/matryer/is"
)

func TestSchema_MarshalUnmarshal(t *testing.T) {
	testCases := []struct {
		name string
		// haveValue is the value we use to extract the schema and which gets marshaled
		haveValue any
		// wantValue is the expected value we get when haveValue gets marshaled and unmarshaled
		wantValue any
		// wantSchema is the schema expected to be extracted from haveValue
		wantSchema avro.Schema
	}{{
		name:       "boolean",
		haveValue:  true,
		wantValue:  true,
		wantSchema: avro.NewPrimitiveSchema(avro.Boolean, nil),
	}, {
		name:      "boolean ptr (false)",
		haveValue: func() *bool { var v bool; return &v }(),
		wantValue: false, // ptr is unmarshalled into value
		wantSchema: must(avro.NewUnionSchema(
			[]avro.Schema{
				avro.NewPrimitiveSchema(avro.Boolean, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			},
		)),
	}, {
		name:      "boolean ptr (nil)",
		haveValue: func() *bool { return nil }(),
		wantValue: nil, // when unmarshaling we get an untyped nil
		wantSchema: must(avro.NewUnionSchema(
			[]avro.Schema{
				avro.NewPrimitiveSchema(avro.Boolean, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			},
		)),
	}, {
		name:       "int",
		haveValue:  int(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "int64",
		haveValue:  int64(1),
		wantValue:  int64(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Long, nil),
	}, {
		name:       "int32",
		haveValue:  int32(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "int16",
		haveValue:  int16(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "int8",
		haveValue:  int8(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "uint32",
		haveValue:  uint32(1),
		wantValue:  int64(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Long, nil),
	}, {
		name:       "uint16",
		haveValue:  uint16(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "uint8",
		haveValue:  uint8(1),
		wantValue:  int(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Int, nil),
	}, {
		name:       "float64",
		haveValue:  float64(1),
		wantValue:  float64(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Double, nil),
	}, {
		name:       "float32",
		haveValue:  float32(1),
		wantValue:  float32(1),
		wantSchema: avro.NewPrimitiveSchema(avro.Float, nil),
	}, {
		name:       "string",
		haveValue:  "1",
		wantValue:  "1",
		wantSchema: avro.NewPrimitiveSchema(avro.String, nil),
	}, {
		name:       "[]byte",
		haveValue:  []byte{1, 2, 3},
		wantValue:  []byte{1, 2, 3},
		wantSchema: avro.NewPrimitiveSchema(avro.Bytes, nil),
	}, {
		name:       "[4]byte",
		haveValue:  [4]byte{1, 2, 3, 4},
		wantValue:  []byte{1, 2, 3, 4}, // fixed is unmarshaled into slice
		wantSchema: must(avro.NewFixedSchema("record.foo", "", 4, nil)),
	}, {
		name:      "nil",
		haveValue: nil,
		wantValue: nil,
		wantSchema: must(avro.NewUnionSchema( // untyped nils default to nullable strings
			[]avro.Schema{
				avro.NewPrimitiveSchema(avro.String, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			},
		)),
	}, {
		name:       "[]int",
		haveValue:  []int{1, 2, 3},
		wantValue:  []any{1, 2, 3},
		wantSchema: avro.NewArraySchema(avro.NewPrimitiveSchema(avro.Int, nil)),
	}, {
		name:      "[]any (with data)",
		haveValue: []any{1, "foo"},
		wantValue: []any{1, "foo"},
		wantSchema: avro.NewArraySchema(must(avro.NewUnionSchema(
			[]avro.Schema{
				avro.NewPrimitiveSchema(avro.String, nil),
				avro.NewPrimitiveSchema(avro.Int, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			},
		))),
	}, {
		name:      "[]any (no data)",
		haveValue: []any{},
		wantValue: []any{},
		wantSchema: avro.NewArraySchema(must(avro.NewUnionSchema( // empty slice values default to nullable strings
			[]avro.Schema{
				avro.NewPrimitiveSchema(avro.String, nil),
				avro.NewPrimitiveSchema(avro.Null, nil),
			},
		))),
	}, {
		name:       "[][]int",
		haveValue:  [][]int{{1}, {2, 3}},
		wantValue:  []any{[]any{1}, []any{2, 3}},
		wantSchema: avro.NewArraySchema(avro.NewArraySchema(avro.NewPrimitiveSchema(avro.Int, nil))),
	}, {
		name: "map[string]int",
		haveValue: map[string]int{
			"foo": 1,
			"bar": 2,
		},
		wantValue: map[string]any{ // all maps are unmarshaled into map[string]any
			"foo": 1,
			"bar": 2,
		},
		wantSchema: avro.NewMapSchema(avro.NewPrimitiveSchema(avro.Int, nil)),
	}, {
		name: "map[string]any (with data)",
		haveValue: map[string]any{
			"foo":  "bar",
			"foo2": "bar2",
			"bar":  1,
			"baz":  []int{1, 2, 3},
			"baz2": []any{"foo", true},
		},
		wantValue: map[string]any{
			"foo":  "bar",
			"foo2": "bar2",
			"bar":  1,
			"baz":  []any{1, 2, 3},
			"baz2": []any{"foo", true},
		},
		wantSchema: avro.NewMapSchema(must(avro.NewUnionSchema([]avro.Schema{
			&avro.NullSchema{},
			avro.NewPrimitiveSchema(avro.Int, nil),
			avro.NewPrimitiveSchema(avro.String, nil),
			avro.NewArraySchema(must(avro.NewUnionSchema([]avro.Schema{
				&avro.NullSchema{},
				avro.NewPrimitiveSchema(avro.Int, nil),
				avro.NewPrimitiveSchema(avro.String, nil),
				avro.NewPrimitiveSchema(avro.Boolean, nil),
			}))),
		}))),
	}, {
		name:      "map[string]any (no data)",
		haveValue: map[string]any{},
		wantValue: map[string]any{},
		wantSchema: avro.NewMapSchema(must(avro.NewUnionSchema([]avro.Schema{ // empty map values default to nullable strings
			&avro.NullSchema{},
			avro.NewPrimitiveSchema(avro.String, nil),
		}))),
	}, {
		name: "map[string]any (nested)",
		haveValue: map[string]any{
			"foo": map[string]any{
				"bar": "baz",
				"baz": 1,
			},
		},
		wantValue: map[string]any{
			"foo": map[string]any{
				"bar": "baz",
				"baz": 1,
			},
		},
		wantSchema: avro.NewMapSchema(must(avro.NewUnionSchema([]avro.Schema{
			&avro.NullSchema{},
			avro.NewMapSchema(must(avro.NewUnionSchema([]avro.Schema{
				&avro.NullSchema{},
				avro.NewPrimitiveSchema(avro.Int, nil),
				avro.NewPrimitiveSchema(avro.String, nil),
			}))),
		}))),
	}, {
		name: "record.StructuredData",
		haveValue: record.StructuredData{
			"foo": "bar",
			"bar": 1,
			"baz": []int{1, 2, 3},
		},
		wantValue: map[string]any{ // structured data is unmarshaled into a map
			"foo": "bar",
			"bar": 1,
			"baz": []any{1, 2, 3},
		},
		wantSchema: must(avro.NewRecordSchema(
			"record.foo",
			"",
			[]*avro.Field{
				must(avro.NewField("foo", avro.NewPrimitiveSchema(avro.String, nil))),
				must(avro.NewField("bar", avro.NewPrimitiveSchema(avro.Int, nil))),
				must(avro.NewField("baz", avro.NewArraySchema(avro.NewPrimitiveSchema(avro.Int, nil)))),
			},
		)),
	}}

	newRecord := func(v any) record.StructuredData {
		return record.StructuredData{"foo": v}
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			// create new record with haveValue in field "foo"
			haveValue := newRecord(tc.haveValue)

			// extract schema and ensure it matches the expectation
			gotSchema, err := SchemaForType(haveValue)
			is.NoErr(err)

			wantSchema := &Schema{
				schema: must(avro.NewRecordSchema("record", "",
					[]*avro.Field{must(avro.NewField("foo", tc.wantSchema))},
				)),
			}
			wantSchema.Sort()
			gotSchema.Sort()
			is.Equal(wantSchema.String(), gotSchema.String())

			// now try to marshal the value with the schema
			bytes, err := gotSchema.Marshal(haveValue)
			is.NoErr(err)

			// unmarshal the bytes back into structured data and compare the value
			var gotValue record.StructuredData
			err = gotSchema.Unmarshal(bytes, &gotValue)
			is.NoErr(err)

			wantValue := newRecord(tc.wantValue)
			is.Equal(wantValue, gotValue)
		})
	}
}

func TestSchemaForType_NestedStructuredData(t *testing.T) {
	is := is.New(t)

	have := record.StructuredData{
		"foo": "bar",
		"level1": record.StructuredData{
			"foo": "bar",
			"level2": record.StructuredData{
				"foo": "bar",
				"level3": record.StructuredData{
					"foo":        "bar",
					"regularMap": map[string]bool{},
				},
			},
		},
	}

	want := &Schema{schema: must(avro.NewRecordSchema(
		"record", "",
		[]*avro.Field{
			must(avro.NewField("foo", avro.NewPrimitiveSchema(avro.String, nil))),
			must(avro.NewField("level1",
				must(avro.NewRecordSchema(
					"record.level1", "",
					[]*avro.Field{
						must(avro.NewField("foo", avro.NewPrimitiveSchema(avro.String, nil))),
						must(avro.NewField("level2",
							must(avro.NewRecordSchema(
								"record.level1.level2", "",
								[]*avro.Field{
									must(avro.NewField("foo", avro.NewPrimitiveSchema(avro.String, nil))),
									must(avro.NewField("level3",
										must(avro.NewRecordSchema(
											"record.level1.level2.level3", "",
											[]*avro.Field{
												must(avro.NewField("foo", avro.NewPrimitiveSchema(avro.String, nil))),
												must(avro.NewField("regularMap", avro.NewMapSchema(
													avro.NewPrimitiveSchema(avro.Boolean, nil),
												))),
											},
										)),
									)),
								},
							)),
						)),
					},
				)),
			)),
		},
	))}
	want.Sort()

	got, err := SchemaForType(have)
	is.NoErr(err)
	is.Equal(want.String(), got.String())

	bytes, err := got.Marshal(have)
	is.NoErr(err)
	// only try to unmarshal to ensure there's no error, other tests assert that
	// umarshaled data matches the expectations
	var unmarshaled record.StructuredData
	err = got.Unmarshal(bytes, &unmarshaled)
	is.NoErr(err)
}

func TestSchemaForType_UnsupportedTypes(t *testing.T) {
	testCases := []struct {
		val     any
		wantErr error
	}{
		// avro only supports fixed byte arrays
		{val: [4]int{}, wantErr: cerrors.New("record: arrays with value type int not supported, avro only supports bytes as values")},
		{val: [4]bool{}, wantErr: cerrors.New("record: arrays with value type bool not supported, avro only supports bytes as values")},
		// avro only supports maps with string keys
		{val: map[int]string{}, wantErr: cerrors.New("record: maps with key type int not supported, avro only supports strings as keys")},
		{val: map[bool]string{}, wantErr: cerrors.New("record: maps with key type bool not supported, avro only supports strings as keys")},
		// avro only supports signed integers
		{val: uint64(1), wantErr: cerrors.New("record: unsupported type: uint64")},
		{val: uint(1), wantErr: cerrors.New("record: unsupported type: uint")},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%T", tc.val), func(t *testing.T) {
			is := is.New(t)
			_, err := SchemaForType(tc.val)
			is.True(err != nil)
			is.Equal(err.Error(), tc.wantErr.Error())
		})
	}
}

func must[T any](f T, err error) T {
	if err != nil {
		panic(err)
	}
	return f
}
