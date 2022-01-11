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
	"strconv"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	SchemaType = "proto"
)

// TODO main todos gathered here
//   - add support for importing other schemas (i.e. schema imports types from other files)
//   - add support for proto2
//   - add support for oneof (do we need it? how do we represent it internally?)
//   - add parameters
//   - add support for converting a generic schema.Schema to a protobuf.Schema
//     - support for recursive fields
//   - add function to parse a []byte to object
//   - add function to encode an object to []byte

type Schema struct {
	fileSet *descriptorpb.FileDescriptorSet

	registry   *protoregistry.Files
	descriptor protoreflect.FileDescriptor
	version    int
}

// NewSchema creates a Schema and uses the provided file descriptor
// as the main file descriptor. Any files that contain imported types
// should be included in the provided file descriptor set (including
// the main descriptor).
func NewSchema(
	fileSet *descriptorpb.FileDescriptorSet,
	mainDescriptorPath string,
	version int,
) (Schema, error) {
	registry, err := protodesc.NewFiles(fileSet)
	if err != nil {
		return Schema{}, cerrors.Errorf("could not create proto registry: %w", err)
	}

	var mainDescriptor protoreflect.FileDescriptor
	//nolint:gocritic // no single value to have a switch on
	if mainDescriptorPath != "" {
		mainDescriptor, err = registry.FindFileByPath(mainDescriptorPath)
		if err != nil {
			return Schema{}, cerrors.Errorf("could not find main descriptor %q: %w", mainDescriptorPath, err)
		}
	} else if registry.NumFiles() == 1 {
		// if there is only 1 file we don't need an explicit main descriptor path
		registry.RangeFiles(func(descriptor protoreflect.FileDescriptor) bool {
			mainDescriptor = descriptor
			return true
		})
	} else {
		return Schema{}, cerrors.New("missing main descriptor path and more than 1 file in registry")
	}

	return Schema{
		fileSet:    fileSet,
		registry:   registry,
		descriptor: mainDescriptor,
		version:    version,
	}, nil
}
func (s Schema) Version() int {
	return s.version
}
func (s Schema) Type() string {
	return SchemaType
}
func (s Schema) Descriptors() []schema.Descriptor {
	descriptorsSize := 0
	descriptorsSize += s.descriptor.Enums().Len()
	descriptorsSize += s.descriptor.Messages().Len()

	if descriptorsSize == 0 {
		return nil
	}

	all := make([]schema.Descriptor, 0, descriptorsSize)

	for i := 0; i < s.descriptor.Messages().Len(); i++ {
		all = append(all, StructDescriptor{s, s.descriptor.Messages().Get(i)})
	}
	for i := 0; i < s.descriptor.Enums().Len(); i++ {
		all = append(all, EnumDescriptor{s.descriptor.Enums().Get(i)})
	}

	return all
}
func (s Schema) ToMutable() schema.MutableSchema {
	return NewMutableSchema(s)
}
func (s Schema) FileDescriptorSet() *descriptorpb.FileDescriptorSet {
	return s.fileSet
}

type StructDescriptor struct {
	schema     Schema
	descriptor protoreflect.MessageDescriptor
}

func (d StructDescriptor) DescriptorType(schema.StructDescriptor) {}
func (d StructDescriptor) Name() string {
	return string(d.descriptor.Name())
}
func (d StructDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d StructDescriptor) Fields() []schema.Field {
	fieldDescriptors := make([]schema.Field, d.descriptor.Fields().Len())
	for i := 0; i < d.descriptor.Fields().Len(); i++ {
		fieldDescriptors[i] = Field{d.schema, d.descriptor.Fields().Get(i)}
	}
	return fieldDescriptors
}

type Field struct {
	schema     Schema
	descriptor protoreflect.FieldDescriptor
}

func (f Field) DescriptorType(schema.Field) {}
func (f Field) Name() string {
	return string(f.descriptor.Name())
}
func (f Field) Index() int {
	return int(f.descriptor.Number())
}
func (f Field) Descriptor() schema.Descriptor {
	if f.descriptor.IsMap() {
		return MapDescriptor{
			descriptor:      f.descriptor.Message(),
			keyDescriptor:   f.extractDescriptor(f.descriptor.MapKey()),
			valueDescriptor: f.extractDescriptor(f.descriptor.MapValue()),
		}
	}

	d := f.extractDescriptor(f.descriptor)
	if f.descriptor.IsList() {
		d = ArrayDescriptor{
			valueDescriptor: d,
		}
	}
	return d
}

// Helper method for retrieving the descriptor for the underlying value.
// This method does not figure out if the value is an array or map.
func (f Field) extractDescriptor(fd protoreflect.FieldDescriptor) schema.Descriptor {
	switch fd.Kind() {
	case protoreflect.MessageKind:
		return StructDescriptor{f.schema, fd.Message()}
	case protoreflect.EnumKind:
		return EnumDescriptor{fd.Enum()}
	default:
		return PrimitiveDescriptor{descriptor: fd}
	}
}

type MapDescriptor struct {
	descriptor      protoreflect.MessageDescriptor
	keyDescriptor   schema.Descriptor
	valueDescriptor schema.Descriptor
}

func (d MapDescriptor) DescriptorType(schema.MapDescriptor) {}
func (d MapDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d MapDescriptor) KeyDescriptor() schema.Descriptor {
	return d.keyDescriptor
}
func (d MapDescriptor) ValueDescriptor() schema.Descriptor {
	return d.valueDescriptor
}

type ArrayDescriptor struct {
	valueDescriptor schema.Descriptor
}

func (d ArrayDescriptor) DescriptorType(schema.ArrayDescriptor) {}
func (d ArrayDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d ArrayDescriptor) ValueDescriptor() schema.Descriptor {
	return d.valueDescriptor
}

type EnumDescriptor struct {
	descriptor protoreflect.EnumDescriptor
}

func (d EnumDescriptor) DescriptorType(descriptor schema.EnumDescriptor) {}
func (d EnumDescriptor) Name() string {
	return string(d.descriptor.Name())
}
func (d EnumDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d EnumDescriptor) ValueDescriptors() []schema.EnumValueDescriptor {
	valueDescriptors := make([]schema.EnumValueDescriptor, d.descriptor.Values().Len())
	for i := 0; i < d.descriptor.Values().Len(); i++ {
		valueDescriptors[i] = EnumValueDescriptor{d.descriptor.Values().Get(i)}
	}
	return valueDescriptors
}

type EnumValueDescriptor struct {
	descriptor protoreflect.EnumValueDescriptor
}

func (d EnumValueDescriptor) DescriptorType(descriptor schema.EnumValueDescriptor) {}
func (d EnumValueDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d EnumValueDescriptor) Value() string {
	return strconv.FormatInt(int64(d.descriptor.Number()), 10)
}
func (d EnumValueDescriptor) Name() string {
	return string(d.descriptor.Name())
}

type PrimitiveDescriptor struct {
	descriptor protoreflect.FieldDescriptor
}

func (d PrimitiveDescriptor) DescriptorType(schema.PrimitiveDescriptor) {}
func (d PrimitiveDescriptor) String() string {
	return fmt.Sprintf("%s (%s)", d.Type(), d.descriptor.Kind())
}
func (d PrimitiveDescriptor) Type() schema.PrimitiveDescriptorType {
	// TODO make map global?
	typeMapping := map[protoreflect.Kind]schema.PrimitiveDescriptorType{
		protoreflect.BoolKind:   schema.Boolean,
		protoreflect.StringKind: schema.String,
		protoreflect.BytesKind:  schema.Bytes,

		protoreflect.FloatKind:  schema.Float32,
		protoreflect.DoubleKind: schema.Float64,

		protoreflect.Int32Kind:    schema.Int32,
		protoreflect.Int64Kind:    schema.Int64,
		protoreflect.Sint32Kind:   schema.Int32,
		protoreflect.Sint64Kind:   schema.Int64,
		protoreflect.Sfixed32Kind: schema.Int32,
		protoreflect.Sfixed64Kind: schema.Int64,

		protoreflect.Uint32Kind:  schema.UInt32,
		protoreflect.Uint64Kind:  schema.UInt64,
		protoreflect.Fixed32Kind: schema.UInt32,
		protoreflect.Fixed64Kind: schema.UInt64,

		// no support for group types
		protoreflect.GroupKind: schema.Unknown,
	}
	return typeMapping[d.descriptor.Kind()]
}
func (d PrimitiveDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
