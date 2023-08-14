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

package proto

import (
	"fmt"
	"github.com/matryer/is"
	"math"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
	"google.golang.org/protobuf/types/descriptorpb"
)

func fileDescriptorSetToMutalbeSchema(t *testing.T, fds *descriptorpb.FileDescriptorSet) *MutableSchema {
	s, err := NewSchema(fds, "", 1)
	assert.Ok(t, err)
	return s.ToMutable().(*MutableSchema)
}

func TestMutableSchema_Type(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, standaloneDescriptorSetPath))
	assert.Equal(t, SchemaType, ms.Type())
}

func TestMutableSchema_SetVersion(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		version int
		wantErr error
	}{
		// we don't validate the version field
		{version: 0, wantErr: nil},
		{version: -1, wantErr: nil},
		{version: 1, wantErr: nil},
		{version: -math.MaxInt32, wantErr: nil},
		{version: math.MaxInt32, wantErr: nil},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, standaloneDescriptorSetPath))
			assert.Equal(t, 1, ms.Version())

			ms.SetVersion(tc.version)
			assert.Equal(t, tc.version, ms.Version())

			newSchema, err := ms.Build()
			is.Equal(tc.wantErr, err)
			assert.Equal(t, tc.version, newSchema.Version())
		})
	}
}

func TestMutableSchema_SetDescriptors_Panics(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		descriptors []schema.MutableDescriptor
		wantPanic   error
	}{{
		descriptors: []schema.MutableDescriptor{
			&MutablePrimitiveDescriptor{},
		},
		wantPanic: cerrors.New("unexpected descriptor type *proto.MutablePrimitiveDescriptor"),
	}, {
		descriptors: []schema.MutableDescriptor{
			&MutableEnumValueDescriptor{},
		},
		wantPanic: cerrors.New("unexpected descriptor type *proto.MutableEnumValueDescriptor"),
	}, {
		descriptors: []schema.MutableDescriptor{
			&MutableArrayDescriptor{},
		},
		wantPanic: cerrors.New("unexpected descriptor type *proto.MutableArrayDescriptor"),
	}, {
		descriptors: []schema.MutableDescriptor{
			&MutableMapDescriptor{},
		},
		wantPanic: cerrors.New("unexpected descriptor type *proto.MutableMapDescriptor"),
	}, {
		descriptors: []schema.MutableDescriptor{
			&MutableStructDescriptor{},
			&MutablePrimitiveDescriptor{},
		},
		wantPanic: cerrors.New("unexpected descriptor type *proto.MutablePrimitiveDescriptor"),
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					is.Equal(tc.wantPanic, r.(error))
				}
			}()

			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, standaloneDescriptorSetPath))
			ms.SetDescriptors(tc.descriptors)
			assert.True(t, false, "expected panic")
		})
	}
}

func TestMutableSchema_SetDescriptors_Empty(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, standaloneDescriptorSetPath))

	ms.SetDescriptors(nil)
	assert.Equal(t, 0, len(ms.Descriptors()))

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	assert.Equal(t, 0, len(newSchema.Descriptors()))
}

func TestMutableSchema_SetDescriptors_Success(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	// only retain Foo, AllTypes and MyEnum
	descriptors := ms.Descriptors()
	fooDesc := descriptors[0].(*MutableStructDescriptor)
	allTypesDesc := descriptors[1].(*MutableStructDescriptor)
	myEnumDesc := descriptors[4].(*MutableEnumDescriptor)

	ms.SetDescriptors([]schema.MutableDescriptor{fooDesc, allTypesDesc, myEnumDesc})

	got := ms.Descriptors()
	assert.Equal(t, 3, len(got))
	assert.Equal(t, fooDesc, got[0])
	assert.Equal(t, allTypesDesc, got[1])
	assert.Equal(t, myEnumDesc, got[2])

	newSchema, err := ms.Build()
	assert.Ok(t, err)

	got = newSchema.Descriptors()
	assert.Equal(t, 3, len(got))
	assert.Equal(t, fooDesc.Name(), got[0].(StructDescriptor).Name())
	assert.Equal(t, fooDesc.Parameters(), got[0].(StructDescriptor).Parameters())
	assert.Equal(t, len(fooDesc.Fields()), len(got[0].(StructDescriptor).Fields()))

	assert.Equal(t, allTypesDesc.Name(), got[1].(StructDescriptor).Name())
	assert.Equal(t, allTypesDesc.Parameters(), got[1].(StructDescriptor).Parameters())
	assert.Equal(t, len(allTypesDesc.Fields()), len(got[1].(StructDescriptor).Fields()))

	assert.Equal(t, myEnumDesc.Name(), got[2].(EnumDescriptor).Name())
	assert.Equal(t, myEnumDesc.Parameters(), got[2].(EnumDescriptor).Parameters())
	assert.Equal(t, len(myEnumDesc.ValueDescriptors()), len(got[2].(EnumDescriptor).ValueDescriptors()))
}

func TestMutableStructDescriptor_SetName_Success(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	emptyDesc := ms.Descriptors()[2].(*MutableStructDescriptor)
	emptyDesc.SetName("EmptyNew")

	assert.Equal(t, "EmptyNew", emptyDesc.Name())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[2].(StructDescriptor)
	assert.Equal(t, "EmptyNew", got.Name())
}

// Test that changing the name of a type that is referenced by other fields
// will produce an error. This can be improved in the future, we can search for
// all references and rename them.
func TestMutableStructDescriptor_SetName_CannotResolveType(t *testing.T) {
	is := is.New(t)

	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	fooDesc := ms.Descriptors()[0].(*MutableStructDescriptor)
	fooDesc.SetName("FooNew")
	assert.Equal(t, "FooNew", fooDesc.Name())

	newSchema, err := ms.Build()
	is.Equal(cerrors.New(`could not create proto registry: proto: message field "proto.AllTypes.f16" cannot resolve type: "proto.Foo" not found`), err)
	assert.Equal(t, nil, newSchema)
}

func TestMutableStructDescriptor_SetFields_NewFieldSuccess(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	fooDesc := ms.Descriptors()[0].(*MutableStructDescriptor)
	fields := fooDesc.Fields()

	myField := NewMutableField(ms, "myField", 3, NewMutablePrimitiveDescriptor(ms, schema.String))
	fields = append(fields, myField)

	// repack
	mutableFields := make([]schema.MutableField, len(fields))
	for i, f := range fields {
		mutableFields[i] = f.(schema.MutableField)
	}

	fooDesc.SetFields(mutableFields)

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	gotFields := newSchema.Descriptors()[0].(StructDescriptor).Fields()
	assert.Equal(t, 3, len(gotFields))
	gotField := gotFields[2]
	assert.Equal(t, "myField", gotField.Name())
	assert.Equal(t, 3, gotField.Index())
}

func TestMutableStructDescriptor_SetFields_NewFieldConflict(t *testing.T) {
	is := is.New(t)

	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	fooDesc := ms.Descriptors()[0].(*MutableStructDescriptor)
	fields := fooDesc.Fields()

	myField := NewMutableField(ms, "fieldWithIndex2", 2, NewMutablePrimitiveDescriptor(ms, schema.String))
	fields = append(fields, myField)

	// repack
	mutableFields := make([]schema.MutableField, len(fields))
	for i, f := range fields {
		mutableFields[i] = f.(schema.MutableField)
	}

	fooDesc.SetFields(mutableFields)

	_, err := ms.Build()
	is.Equal(cerrors.New(`could not create proto registry: proto: message "proto.Foo" has conflicting fields: "fieldWithIndex2" with "value"`), err)
}

func TestMutableField_SetName(t *testing.T) {

	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	f1Desc := ms.Descriptors()[0].(*MutableStructDescriptor).Fields()[0].(*MutableField)
	f1Desc.SetName("renamedField")

	assert.Equal(t, "renamedField", f1Desc.Name())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[0].(StructDescriptor).Fields()[0]
	assert.Equal(t, "renamedField", got.Name())
}

func TestMutableField_SetIndex(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	f1Desc := ms.Descriptors()[0].(*MutableStructDescriptor).Fields()[0].(*MutableField)
	f1Desc.SetIndex(1234)

	assert.Equal(t, 1234, f1Desc.Index())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[0].(StructDescriptor).Fields()[0]
	assert.Equal(t, 1234, got.Index())
}

func TestMutableField_SetDescriptor_Primitive(t *testing.T) {
	testCases := []schema.PrimitiveDescriptorType{
		schema.Boolean,
		schema.Bytes,
		schema.String,
		schema.Int32,
		schema.Int64,
		schema.UInt32,
		schema.UInt64,
		schema.Float32,
		schema.Float64,
	}

	for _, tc := range testCases {
		t.Run(tc.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f1Desc := ms.Descriptors()[0].(*MutableStructDescriptor).Fields()[0].(*MutableField)
			f1Desc.SetDescriptor(NewMutablePrimitiveDescriptor(ms, tc))

			newSchema, err := ms.Build()
			assert.Ok(t, err)

			got := newSchema.Descriptors()[0].(StructDescriptor).Fields()[0].Descriptor()
			d, ok := got.(PrimitiveDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, got)
			assert.Equal(t, tc, d.Type())
		})
	}
}

func TestMutableField_SetDescriptor_Reference(t *testing.T) {
	testCases := []struct {
		mutableDescriptor func(*MutableSchema) schema.MutableDescriptor
		assertDescriptor  func(*testing.T, schema.Descriptor)
	}{{
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[0].(schema.MutableDescriptor) // Foo
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "Foo", d.Name())
		},
	}, {
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[4].(schema.MutableDescriptor) // MyEnum
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "MyEnum", d.Name())
		},
	}, {
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return NewMutableArrayDescriptor(s, NewMutablePrimitiveDescriptor(s, schema.String))
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(ArrayDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			pd, ok := d.ValueDescriptor().(PrimitiveDescriptor)
			assert.True(t, ok, "expected %T, got %T", pd, d.ValueDescriptor())
			assert.Equal(t, schema.String, pd.Type())
		},
		// TODO add test for maps once we support creating one out of thin air
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			d := tc.mutableDescriptor(ms)
			f1Desc := ms.Descriptors()[0].(*MutableStructDescriptor).Fields()[0].(*MutableField)
			f1Desc.SetDescriptor(d)

			newSchema, err := ms.Build()
			assert.Ok(t, err)

			got := newSchema.Descriptors()[0].(StructDescriptor).Fields()[0]
			tc.assertDescriptor(t, got.Descriptor())
		})
	}
}

func TestMutableMapDescriptor_SetKeyDescriptor_Success(t *testing.T) {
	testCases := []schema.PrimitiveDescriptorType{
		schema.Boolean,
		schema.String,
		schema.Int32,
		schema.Int64,
		schema.UInt32,
		schema.UInt64,
	}

	for _, tc := range testCases {
		t.Run(tc.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f18desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[17]
			mapDesc := f18desc.Descriptor().(*MutableMapDescriptor)

			keyDesc := NewMutablePrimitiveDescriptor(ms, tc)
			mapDesc.SetKeyDescriptor(keyDesc)

			assert.Equal(t, keyDesc, mapDesc.keyDescriptor)

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotMapDesc := newSchema.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor().(MapDescriptor)
			gotKeyDesc := gotMapDesc.KeyDescriptor().(PrimitiveDescriptor)

			assert.Equal(t, tc, gotKeyDesc.Type())
		})
	}
}

func TestMutableMapDescriptor_SetKeyDescriptor_InvalidKeyKind(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		descriptorType schema.PrimitiveDescriptorType
		wantErr        error
	}{{
		descriptorType: schema.Bytes,
		wantErr:        cerrors.New(`could not create proto registry: proto: message field "proto.AllTypes.f18" is an invalid map: invalid key kind: bytes`),
	}, {
		descriptorType: schema.Float32,
		wantErr:        cerrors.New(`could not create proto registry: proto: message field "proto.AllTypes.f18" is an invalid map: invalid key kind: float`),
	}, {
		descriptorType: schema.Float64,
		wantErr:        cerrors.New(`could not create proto registry: proto: message field "proto.AllTypes.f18" is an invalid map: invalid key kind: double`),
	}, {
		descriptorType: schema.Unknown,
		wantErr:        cerrors.New(`could not create proto registry: proto: message field "proto.AllTypes.F18Entry.key" cannot resolve type: invalid name reference: ""`),
	}}

	for _, tc := range testCases {
		t.Run(tc.descriptorType.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f18desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[17]
			mapDesc := f18desc.Descriptor().(*MutableMapDescriptor)

			keyDesc := NewMutablePrimitiveDescriptor(ms, tc.descriptorType)
			mapDesc.SetKeyDescriptor(keyDesc)

			assert.Equal(t, keyDesc, mapDesc.KeyDescriptor())

			_, err := ms.Build()
			is.Equal(tc.wantErr, err)
		})
	}
}

func TestMutableMapDescriptor_SetValueDescriptor_Primitive(t *testing.T) {
	testCases := []schema.PrimitiveDescriptorType{
		schema.Boolean,
		schema.Bytes,
		schema.String,
		schema.Int32,
		schema.Int64,
		schema.UInt32,
		schema.UInt64,
		schema.Float32,
		schema.Float64,
	}

	for _, tc := range testCases {
		t.Run(tc.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f18desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[17]
			mapDesc := f18desc.Descriptor().(*MutableMapDescriptor)

			valDesc := NewMutablePrimitiveDescriptor(ms, tc)
			mapDesc.SetValueDescriptor(valDesc)

			assert.Equal(t, valDesc, mapDesc.ValueDescriptor())

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotMapDesc := newSchema.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor().(MapDescriptor)
			gotValDesc := gotMapDesc.ValueDescriptor().(PrimitiveDescriptor)

			assert.Equal(t, tc, gotValDesc.Type())
		})
	}
}

func TestMutableMapDescriptor_SetValueDescriptor_Reference(t *testing.T) {
	testCases := []struct {
		mutableDescriptor func(*MutableSchema) schema.MutableDescriptor
		assertDescriptor  func(*testing.T, schema.Descriptor)
	}{{
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[0].(schema.MutableDescriptor) // Foo
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "Foo", d.Name())
		},
	}, {
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[4].(schema.MutableDescriptor) // MyEnum
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "MyEnum", d.Name())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f18desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[17]
			mapDesc := f18desc.Descriptor().(*MutableMapDescriptor)

			valDesc := tc.mutableDescriptor(ms)
			mapDesc.SetValueDescriptor(valDesc)

			assert.Equal(t, valDesc, mapDesc.ValueDescriptor())

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotDesc := newSchema.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor().(MapDescriptor).valueDescriptor
			tc.assertDescriptor(t, gotDesc)
		})
	}
}

func TestMutableArrayDescriptor_SetValueDescriptor_Primitive(t *testing.T) {
	testCases := []schema.PrimitiveDescriptorType{
		schema.Boolean,
		schema.Bytes,
		schema.String,
		schema.Int32,
		schema.Int64,
		schema.UInt32,
		schema.UInt64,
		schema.Float32,
		schema.Float64,
	}

	for _, tc := range testCases {
		t.Run(tc.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f17desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[16]
			arrayDesc := f17desc.Descriptor().(*MutableArrayDescriptor)

			valDesc := NewMutablePrimitiveDescriptor(ms, tc)
			arrayDesc.SetValueDescriptor(valDesc)

			assert.Equal(t, valDesc, arrayDesc.ValueDescriptor())

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotArrayDesc := newSchema.Descriptors()[1].(StructDescriptor).Fields()[16].Descriptor().(ArrayDescriptor)
			gotValDesc := gotArrayDesc.ValueDescriptor().(PrimitiveDescriptor)

			assert.Equal(t, tc, gotValDesc.Type())
		})
	}
}

func TestMutableArrayDescriptor_SetValueDescriptor_Reference(t *testing.T) {
	testCases := []struct {
		mutableDescriptor func(*MutableSchema) schema.MutableDescriptor
		assertDescriptor  func(*testing.T, schema.Descriptor)
	}{{
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[0].(schema.MutableDescriptor) // Foo
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "Foo", d.Name())
		},
	}, {
		mutableDescriptor: func(s *MutableSchema) schema.MutableDescriptor {
			return s.Descriptors()[4].(schema.MutableDescriptor) // MyEnum
		},
		assertDescriptor: func(t *testing.T, descriptor schema.Descriptor) {
			d, ok := descriptor.(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", d, descriptor)
			assert.Equal(t, "MyEnum", d.Name())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			f17desc := ms.Descriptors()[1].(*MutableStructDescriptor).Fields()[16]
			arrayDesc := f17desc.Descriptor().(*MutableArrayDescriptor)

			valDesc := tc.mutableDescriptor(ms)
			arrayDesc.SetValueDescriptor(valDesc)

			assert.Equal(t, valDesc, arrayDesc.ValueDescriptor())

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotDesc := newSchema.Descriptors()[1].(StructDescriptor).Fields()[16].Descriptor().(ArrayDescriptor).valueDescriptor
			tc.assertDescriptor(t, gotDesc)
		})
	}
}

func TestMutableEnumDescriptor_SetName_Success(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor)
	enumDesc.SetName("UnusedEnumNew")

	assert.Equal(t, "UnusedEnumNew", enumDesc.Name())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[5].(EnumDescriptor)
	assert.Equal(t, "UnusedEnumNew", got.Name())
}

func TestMutableEnumDescriptor_SetValueDescriptors_NewValueSuccess(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor)
	values := enumDesc.ValueDescriptors()

	myValue := NewMutableEnumValueDescriptor(ms, "myValue", 3)
	values = append(values, myValue)

	// repack
	mutableValues := make([]schema.MutableEnumValueDescriptor, len(values))
	for i, f := range values {
		mutableValues[i] = f.(schema.MutableEnumValueDescriptor)
	}

	enumDesc.SetValueDescriptors(mutableValues)

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	gotValues := newSchema.Descriptors()[5].(EnumDescriptor).ValueDescriptors()
	assert.Equal(t, 3, len(gotValues))
	gotValue := gotValues[2]
	assert.Equal(t, "myValue", gotValue.Name())
	assert.Equal(t, "3", gotValue.Value())
}

func TestMutableEnumDescriptor_SetValues_NewValueConflict(t *testing.T) {
	is := is.New(t)

	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor)
	values := enumDesc.ValueDescriptors()

	myValue := NewMutableEnumValueDescriptor(ms, "value0", 0)
	values = append(values, myValue)

	// repack
	mutableValues := make([]schema.MutableEnumValueDescriptor, len(values))
	for i, f := range values {
		mutableValues[i] = f.(schema.MutableEnumValueDescriptor)
	}

	enumDesc.SetValueDescriptors(mutableValues)

	_, err := ms.Build()
	is.Equal(cerrors.New(`could not create proto registry: proto: enum "proto.UnusedEnum" has conflicting non-aliased values on number 0: "value0" with "V1"`), err)
}

func TestMutableEnumValueDescriptor_SetName_Success(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor).ValueDescriptors()[0].(*MutableEnumValueDescriptor)
	enumDesc.SetName("V0New")

	assert.Equal(t, "V0New", enumDesc.Name())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[5].(EnumDescriptor).ValueDescriptors()[0]
	assert.Equal(t, "V0New", got.Name())
}

func TestMutableEnumValueDescriptor_SetValue_Success(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor).ValueDescriptors()[1].(*MutableEnumValueDescriptor)
	enumDesc.SetValue("1")

	assert.Equal(t, "1", enumDesc.Value())

	newSchema, err := ms.Build()
	assert.Ok(t, err)
	got := newSchema.Descriptors()[5].(EnumDescriptor).ValueDescriptors()[1]
	assert.Equal(t, "1", got.Value())
}

func TestMutableEnumValueDescriptor_SetValue_MissingZeroNumber(t *testing.T) {
	is := is.New(t)

	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	enumDesc := ms.Descriptors()[5].(*MutableEnumDescriptor).ValueDescriptors()[0].(*MutableEnumValueDescriptor)
	enumDesc.SetValue("1")

	assert.Equal(t, "1", enumDesc.Value())

	_, err := ms.Build()
	is.Equal(cerrors.New(`could not create proto registry: proto: enum "proto.V1" using proto3 semantics must have zero number for the first value`), err)
}

func TestMutablePrimitiveDescriptor_Type(t *testing.T) {
	ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

	allTypesDesc := ms.Descriptors()[1].(*MutableStructDescriptor)
	wantTypes := map[int]schema.PrimitiveDescriptorType{
		0:  schema.Boolean, // bool
		1:  schema.String,  // string
		2:  schema.Bytes,   // bytes
		3:  schema.Float32, // float
		4:  schema.Float64, // double
		5:  schema.Int32,   // int32
		6:  schema.Int64,   // int64
		7:  schema.Int32,   // sint32
		8:  schema.Int64,   // sint64
		9:  schema.Int32,   // sfixed32
		10: schema.Int64,   // sfixed64
		11: schema.UInt32,  // uint32
		12: schema.UInt64,  // uint64
		13: schema.UInt32,  // fixed32
		14: schema.UInt64,  // fixed64
	}

	for index, wantType := range wantTypes {
		t.Run(fmt.Sprintf("f%d", index+1), func(t *testing.T) {
			pd, ok := allTypesDesc.Fields()[index].Descriptor().(*MutablePrimitiveDescriptor)
			assert.True(t, ok, fmt.Sprintf("expected %T, got %T", pd, allTypesDesc.Fields()[index].Descriptor()))
			assert.Equal(t, wantType, pd.Type())
		})
	}
}

func TestMutablePrimitiveDescriptor_SetType(t *testing.T) {
	testCases := []schema.PrimitiveDescriptorType{
		schema.Boolean,
		schema.Bytes,
		schema.String,
		schema.Int32,
		schema.Int64,
		schema.UInt32,
		schema.UInt64,
		schema.Float32,
		schema.Float64,
	}

	for _, tc := range testCases {
		t.Run(tc.String(), func(t *testing.T) {
			ms := fileDescriptorSetToMutalbeSchema(t, getFileDescriptorSet(t, test1DescriptorSetPath))

			allTypesDesc := ms.Descriptors()[1].(*MutableStructDescriptor)
			for i, f := range allTypesDesc.Fields() {
				if i == 15 {
					// only first 15 fields are primitive types
					break
				}
				d := f.Descriptor().(*MutablePrimitiveDescriptor)
				d.SetType(tc)

				assert.Equal(t, tc, d.Type())
			}

			newSchema, err := ms.Build()
			assert.Ok(t, err)
			gotAllTypesDesc := newSchema.Descriptors()[1].(StructDescriptor)
			for i, f := range gotAllTypesDesc.Fields() {
				if i == 15 {
					// only first 15 fields are primitive types
					break
				}
				assert.Equal(t, tc, f.Descriptor().(PrimitiveDescriptor).Type())
			}
		})
	}
}
