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

//go:generate mockgen -destination=mock/schema.go -package=mock -mock_names=Schema=Schema,StructDescriptor=StructDescriptor,Field=Field,MapDescriptor=MapDescriptor,ArrayDescriptor=ArrayDescriptor,PrimitiveDescriptor=PrimitiveDescriptor,EnumDescriptor=EnumDescriptor,EnumValueDescriptor=EnumValueDescriptor . Schema,StructDescriptor,Field,MapDescriptor,ArrayDescriptor,PrimitiveDescriptor,EnumDescriptor,EnumValueDescriptor

package schema

import (
	"fmt"
	"strings"
)

const (
	Unknown PrimitiveDescriptorType = iota

	Int32
	Int64

	UInt32
	UInt64

	Float32
	Float64

	Boolean
	String
	Bytes
)

// Schema represents an immutable schema.
type Schema interface {
	// Type returns the schema type (e.g. protobuf).
	Type() string
	// Version represents the schema version. A higher version represents a
	// newer schema.
	Version() int
	// Descriptors returns descriptors defined at the root of this schema.
	Descriptors() []Descriptor
	// ToMutable returns a MutableSchema, that contains the same properties and
	// descriptors as this Schema, except that all of them are mutable.
	ToMutable() MutableSchema

	// TODO add parsing and encoding
	// Parse([]byte, string) (Value, error)
	// Encode(Value, string) ([]byte, error)
}

// Descriptor represents a Descriptor for a single type. Any Descriptor has to
// implement at least one of the following interfaces:
//  - StructDescriptor or MutableStructDescriptor
//  - MapDescriptor or MutableMapDescriptor
//  - ArrayDescriptor or MutableArrayDescriptor
//  - PrimitiveDescriptor or MutablePrimitiveDescriptor
//  - EnumDescriptor or MutableEnumDescriptor
//  - EnumValueDescriptor or MutableEnumValueDescriptor
type Descriptor interface {
	// Parameters returns additional settings specific for the schema type.
	Parameters() map[string]interface{}
}

// StructDescriptor defines a struct type. A struct is a type with a name and 0
// or more fields.
type StructDescriptor interface {
	Descriptor
	isStructDescriptor

	// Name returns the local name of the struct.
	Name() string
	// Fields returns a slice of fields defined in this struct.
	Fields() []Field
}
type isStructDescriptor interface{ DescriptorType(StructDescriptor) }

// Field defines a single field inside of a StructDescriptor. It has a name, an
// index and a descriptor for the underlying value.
type Field interface {
	isField

	// Name returns the name of the field in its parent struct. The name of a
	// field has to be unique among all field names in a struct.
	Name() string
	// Index returns the numeric identifier of the field. The index of a field
	// has to be unique among all field indexes in a struct. NB: field indexes
	// are not necessarily consecutive (e.g. in protobuf schemas an index
	// represents the field number).
	Index() int
	// Descriptor returns the descriptor for the underlying field value.
	Descriptor() Descriptor
}
type isField interface{ DescriptorType(Field) }

// MapDescriptor defines a map with a single key and a value.
type MapDescriptor interface {
	Descriptor
	isMapDescriptor

	// KeyDescriptor returns the descriptor for a map key.
	KeyDescriptor() Descriptor
	// KeyDescriptor returns the descriptor for a map value.
	ValueDescriptor() Descriptor
}
type isMapDescriptor interface{ DescriptorType(MapDescriptor) }

// ArrayDescriptor defines an array of values.
type ArrayDescriptor interface {
	Descriptor
	isArrayDescriptor

	// ValueDescriptor returns the descriptor for a value stored in the array.
	ValueDescriptor() Descriptor
}
type isArrayDescriptor interface{ DescriptorType(ArrayDescriptor) }

// PrimitiveDescriptor defines a value of a primitive type. Primitive types are
// constants of the type PrimitiveDescriptorType.
type PrimitiveDescriptor interface {
	Descriptor
	isPrimitiveDescriptor

	// Type returns the PrimitiveDescriptorType of the primitive value.
	Type() PrimitiveDescriptorType
}
type isPrimitiveDescriptor interface{ DescriptorType(PrimitiveDescriptor) }

// PrimitiveDescriptorType represents the type of a PrimitiveDescriptor.
type PrimitiveDescriptorType int

// String formats PrimitiveDescriptorType to a human readable string.
func (st PrimitiveDescriptorType) String() string {
	switch st {
	case Int32:
		return "int32"
	case Int64:
		return "int64"
	case UInt32:
		return "uint32"
	case UInt64:
		return "uint64"
	case Float32:
		return "float32"
	case Float64:
		return "float64"
	case Boolean:
		return "bool"
	case String:
		return "string"
	case Bytes:
		return "[]byte"
	default:
		return fmt.Sprintf("<unknown:%d>", st)
	}
}

// EnumDescriptor defines an enumeration type with a finite set of possible
// values.
type EnumDescriptor interface {
	Descriptor
	isEnumDescriptor

	// Name returns the local name of the enum.
	Name() string
	// ValueDescriptors returns a slice of descriptors that define all possible
	// enum values.
	ValueDescriptors() []EnumValueDescriptor
}
type isEnumDescriptor interface{ DescriptorType(EnumDescriptor) }

// EnumValueDescriptor defines a single enumeration value inside of an
// EnumDescriptor. It has a name and a value.
type EnumValueDescriptor interface {
	Descriptor
	isEnumValueDescriptor

	// Name returns the local name of the enum value.
	Name() string
	// Value returns the value of the enum value.
	Value() string
}
type isEnumValueDescriptor interface{ DescriptorType(EnumValueDescriptor) }

// ToString converts Schema into a human readable string.
func ToString(s Schema) string {
	if v, ok := s.(fmt.Stringer); ok {
		// allow schema to specify how to format it
		return v.String()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("schema: %s v%d", s.Type(), s.Version()))
	for _, d := range s.Descriptors() {
		str := DescriptorToString(d)
		sb.WriteString("\n" + str + "\n")
	}
	return sb.String()
}

// DescriptorToString converts Descriptor into a human readable string.
func DescriptorToString(d Descriptor) string {
	switch v := d.(type) {
	case fmt.Stringer:
		// allow descriptor to specify how to format it
		return v.String()
	case StructDescriptor:
		return structDescriptorToString(v)
	case MapDescriptor:
		return mapDescriptorToString(v)
	case ArrayDescriptor:
		return arrayDescriptorToString(v)
	case EnumDescriptor:
		return enumDescriptorToString(v)
	case EnumValueDescriptor:
		return enumValueDescriptorToString(v)
	case PrimitiveDescriptor:
		return primitiveDescriptorToString(v)
	}
	return fmt.Sprintf("<unknown:%T>", d)
}

// FieldToString converts Field into a human readable string.
func FieldToString(f Field) string {
	var sb strings.Builder
	sb.WriteString(f.Name())
	sb.WriteString(": ")
	sb.WriteString(DescriptorToString(f.Descriptor()))
	return sb.String()
}

func structDescriptorToString(d StructDescriptor) string {
	var sb strings.Builder
	sb.WriteString("struct ")
	sb.WriteString(d.Name())
	for _, f := range d.Fields() {
		str := FieldToString(f)
		str = strings.ReplaceAll(str, "\n", "\n  ")
		sb.WriteString("\n  " + str)
	}
	return sb.String()
}

func mapDescriptorToString(d MapDescriptor) string {
	var sb strings.Builder
	sb.WriteString("map[")
	sb.WriteString(DescriptorToString(d.KeyDescriptor()))
	sb.WriteString("]")
	sb.WriteString(DescriptorToString(d.ValueDescriptor()))
	return sb.String()
}

func arrayDescriptorToString(d ArrayDescriptor) string {
	str := DescriptorToString(d.ValueDescriptor())
	if strings.ContainsRune(str, '\n') {
		str = strings.ReplaceAll(str, "\n", "\n  ")
		str = fmt.Sprintf("\n  %s\n", str)
	}
	return fmt.Sprintf("[](%s)", str)
}

func enumDescriptorToString(d EnumDescriptor) string {
	var sb strings.Builder
	sb.WriteString("enum ")
	sb.WriteString(d.Name())

	for _, evd := range d.ValueDescriptors() {
		sb.WriteString("\n  ")
		sb.WriteString(DescriptorToString(evd))
	}

	return sb.String()
}

func enumValueDescriptorToString(d EnumValueDescriptor) string {
	return fmt.Sprintf("%s: %s", d.Value(), d.Name())
}

func primitiveDescriptorToString(d PrimitiveDescriptor) string {
	return d.Type().String()
}
