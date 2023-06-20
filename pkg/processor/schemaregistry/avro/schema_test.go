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

	"github.com/conduitio/conduit/pkg/record"
	"github.com/hamba/avro/v2"
	"github.com/matryer/is"
)

func TestSchemaForType_Types(t *testing.T) {
	is := is.New(t)

	type customBool bool
	have := struct {
		MyBool       bool
		MyBoolPtr    *bool
		MyCustomBool customBool

		MyInt   int
		MyInt64 int64
		MyInt32 int32
		MyInt16 int16
		MyInt8  int8

		// uint and uint64 are not supported
		MyUint32 uint32
		MyUint16 uint16
		MyUint8  uint8

		MyFloat32 float32
		MyFloat64 float64

		MyString     string
		MyBytesSlice []byte
		MyBytesArray [4]byte
		MyMap        map[string]float32
		MySlice      []bool
		MyStruct     struct{ Foo int }
	}{}

	want := &Schema{schema: must(avro.NewRecordSchema(
		"record",
		"",
		[]*avro.Field{
			must(avro.NewField("MyBool", avro.NewPrimitiveSchema(avro.Boolean, nil))),
			must(avro.NewField("MyBoolPtr", must(avro.NewUnionSchema(
				[]avro.Schema{
					avro.NewPrimitiveSchema(avro.Boolean, nil),
					avro.NewPrimitiveSchema(avro.Null, nil),
				},
			)))),
			must(avro.NewField("MyCustomBool", avro.NewPrimitiveSchema(avro.Boolean, nil))),
			must(avro.NewField("MyInt", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt64", avro.NewPrimitiveSchema(avro.Long, nil))),
			must(avro.NewField("MyInt32", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt16", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt8", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyUint32", avro.NewPrimitiveSchema(avro.Long, nil))),
			must(avro.NewField("MyUint16", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyUint8", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyFloat32", avro.NewPrimitiveSchema(avro.Float, nil))),
			must(avro.NewField("MyFloat64", avro.NewPrimitiveSchema(avro.Double, nil))),
			must(avro.NewField("MyString", avro.NewPrimitiveSchema(avro.String, nil))),
			must(avro.NewField("MyBytesSlice", avro.NewPrimitiveSchema(avro.Bytes, nil))),
			must(avro.NewField("MyBytesArray", must(
				avro.NewFixedSchema("record.MyBytesArray", "", 4, nil),
			))),
			must(avro.NewField("MyMap",
				avro.NewMapSchema(avro.NewPrimitiveSchema(avro.Float, nil)),
			)),
			must(avro.NewField("MySlice",
				avro.NewArraySchema(avro.NewPrimitiveSchema(avro.Boolean, nil)),
			)),
			must(avro.NewField("MyStruct",
				must(avro.NewRecordSchema(
					"record.MyStruct",
					"",
					[]*avro.Field{
						must(avro.NewField("Foo", avro.NewPrimitiveSchema(avro.Int, nil))),
					},
				)),
			)),
		},
	))}

	got, err := SchemaForType(have)
	is.NoErr(err)
	is.Equal(want.String(), got.String())

	_, err = got.Marshal(have)
	is.NoErr(err)
}

func TestSchemaForType_MapVsStructuredData(t *testing.T) {
	is := is.New(t)

	have := struct {
		MapData        map[string]any
		StructuredData record.StructuredData
	}{}

	want := &Schema{schema: must(avro.NewRecordSchema(
		"record",
		"",
		[]*avro.Field{
			must(avro.NewField("MapData",
				avro.NewMapSchema(&avro.NullSchema{}),
			)),
			must(avro.NewField("StructuredData",
				must(avro.NewRecordSchema("record.StructuredData", "", nil)),
			)),
		},
	))}

	got, err := SchemaForType(have)
	is.NoErr(err)
	is.Equal(want.String(), got.String())

	_, err = got.Marshal(have)
	is.NoErr(err)
}

func TestSchemaForType_StructuredData(t *testing.T) {
	is := is.New(t)

	type customBool bool
	have := record.StructuredData{
		"MyBool":       false,
		"MyBoolPtr":    (*bool)(nil),
		"MyCustomBool": customBool(true),

		"MyInt":   int(1),
		"MyInt64": int64(1),
		"MyInt32": int32(1),
		"MyInt16": int16(1),
		"MyInt8":  int8(1),

		// uint and uint64 are not supported
		"MyUint32": uint32(1),
		"MyUint16": uint16(1),
		"MyUint8":  uint8(1),

		"MyFloat32": float32(1.2),
		"MyFloat64": float64(1.2),

		"MyString":     "foo",
		"MyBytesSlice": []byte{},
		"MyBytesArray": [4]byte{},
		"MyNil":        nil,

		"MyMap":    map[string]float32{},
		"MySlice":  []bool{},
		"MyStruct": struct{ Foo int }{},
	}

	want := &Schema{schema: must(avro.NewRecordSchema(
		"record", "",
		[]*avro.Field{
			must(avro.NewField("MyBool", avro.NewPrimitiveSchema(avro.Boolean, nil))),
			must(avro.NewField("MyBoolPtr", must(avro.NewUnionSchema(
				[]avro.Schema{
					avro.NewPrimitiveSchema(avro.Boolean, nil),
					avro.NewPrimitiveSchema(avro.Null, nil),
				},
			)))),
			must(avro.NewField("MyCustomBool", avro.NewPrimitiveSchema(avro.Boolean, nil))),
			must(avro.NewField("MyInt", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt64", avro.NewPrimitiveSchema(avro.Long, nil))),
			must(avro.NewField("MyInt32", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt16", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyInt8", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyUint32", avro.NewPrimitiveSchema(avro.Long, nil))),
			must(avro.NewField("MyUint16", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyUint8", avro.NewPrimitiveSchema(avro.Int, nil))),
			must(avro.NewField("MyFloat32", avro.NewPrimitiveSchema(avro.Float, nil))),
			must(avro.NewField("MyFloat64", avro.NewPrimitiveSchema(avro.Double, nil))),
			must(avro.NewField("MyString", avro.NewPrimitiveSchema(avro.String, nil))),
			must(avro.NewField("MyBytesSlice", avro.NewPrimitiveSchema(avro.Bytes, nil))),
			must(avro.NewField("MyBytesArray", must(
				avro.NewFixedSchema("record.MyBytesArray", "", 4, nil),
			))),
			must(avro.NewField("MyNil", &avro.NullSchema{})),
			must(avro.NewField("MyMap",
				avro.NewMapSchema(avro.NewPrimitiveSchema(avro.Float, nil)),
			)),
			must(avro.NewField("MySlice", avro.NewArraySchema(
				avro.NewPrimitiveSchema(avro.Boolean, nil),
			))),
			must(avro.NewField("MyStruct",
				must(avro.NewRecordSchema(
					"record.MyStruct",
					"",
					[]*avro.Field{
						must(avro.NewField("Foo", avro.NewPrimitiveSchema(avro.Int, nil))),
					},
				)),
			)),
		},
	))}

	got, err := SchemaForType(have)
	is.NoErr(err)

	want.Sort()
	got.Sort()

	is.Equal(want.String(), got.String())

	_, err = got.Marshal(have)
	is.NoErr(err)
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

	got, err := SchemaForType(have)
	is.NoErr(err)

	want.Sort()
	got.Sort()

	is.Equal(want.String(), got.String())

	_, err = got.Marshal(have)
	is.NoErr(err)
}

func TestSchemaForType_UnsupportedTypes(t *testing.T) {
	testCases := []any{
		// avro only supports fixed byte arrays
		[4]int{},
		[4]bool{},
		// avro only supports maps with string keys
		map[int]string{},
		map[bool]string{},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%T", tc), func(t *testing.T) {
			is := is.New(t)
			_, err := SchemaForType(tc)
			is.True(err != nil)
		})
	}
}

func must[T any](f T, err error) T {
	if err != nil {
		panic(err)
	}
	return f
}
