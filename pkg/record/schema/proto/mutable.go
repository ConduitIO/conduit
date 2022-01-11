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
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TODO figure out how to handle the following methods:
//   - MutableField.SetDescriptor
//   - MutableMapDescriptor.SetKeyDescriptor
//   - MutableMapDescriptor.SetValueDescriptor
//   - ArrayDescriptor.SetValueDescriptor
//   These methods need to make sure that the supplied descriptor will be tied
//   to the same underlying Proto descriptor object. This means that a Setter
//   will inevitably change the incoming parameter, and that's not nice.
//   Maybe that's why the protobuf implementation is using only a
//   FieldDescriptorProto to describe any field (primitive, map, array,
//   reference)...

type MutableSchema struct {
	base Schema

	version    int
	descriptor *descriptorpb.FileDescriptorProto
	changed    bool

	children []schema.MutableDescriptor
	once     sync.Once
}

func NewMutableSchema(base Schema) *MutableSchema {
	// This is the only place we should convert a descriptor to descriptor proto.
	// We need to make sure that all other descriptors are taken from this proto
	// to ensure changes are visible in the file descriptor proto.
	fdp := protodesc.ToFileDescriptorProto(base.descriptor)
	return &MutableSchema{
		base:       base,
		version:    base.version,
		descriptor: fdp,
	}
}

func (s *MutableSchema) Type() string {
	return SchemaType
}
func (s *MutableSchema) Version() int {
	return s.version
}
func (s *MutableSchema) Descriptors() []schema.Descriptor {
	s.once.Do(func() {
		descriptorsSize := 0
		descriptorsSize += len(s.descriptor.EnumType)
		descriptorsSize += len(s.descriptor.MessageType)

		if descriptorsSize == 0 {
			return
		}

		all := make([]schema.MutableDescriptor, 0, descriptorsSize)

		for _, dp := range s.descriptor.MessageType {
			all = append(all, &MutableStructDescriptor{
				schema:     s,
				descriptor: dp,
			})
		}
		for _, dp := range s.descriptor.EnumType {
			all = append(all, &MutableEnumDescriptor{
				schema:     s,
				descriptor: dp,
			})
		}

		s.children = all
	})

	// repack
	tmp := make([]schema.Descriptor, len(s.children))
	for i, v := range s.children {
		tmp[i] = v
	}
	return tmp
}
func (s *MutableSchema) SetVersion(v int) {
	// no need to set changed to true
	s.version = v
}
func (s *MutableSchema) SetDescriptors(descriptors []schema.MutableDescriptor) {
	s.once.Do(func() { /* ensure we don't initialize descriptors anymore */ })
	s.changed = true

	s.descriptor.MessageType = s.descriptor.MessageType[:0]
	s.descriptor.EnumType = s.descriptor.EnumType[:0]

	for _, d := range descriptors {
		switch v := d.(type) {
		case *MutableStructDescriptor:
			s.descriptor.MessageType = append(s.descriptor.MessageType, v.descriptor)
		case *MutableEnumDescriptor:
			s.descriptor.EnumType = append(s.descriptor.EnumType, v.descriptor)
		default:
			panic(cerrors.Errorf("unexpected descriptor type %T", d))
		}
	}

	s.children = descriptors
}

func (s *MutableSchema) Build() (schema.Schema, error) {
	if !s.changed {
		s.base.version = s.Version()
		return s.base, nil
	}

	var fileSet descriptorpb.FileDescriptorSet

	fileSet.File = make([]*descriptorpb.FileDescriptorProto, 0, s.base.registry.NumFiles())

	s.base.registry.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		fdp := protodesc.ToFileDescriptorProto(fd)
		if fd == s.base.descriptor {
			fdp = s.descriptor // switch in our local descriptor
		}
		fileSet.File = append(fileSet.File, fdp)
		return true
	})

	out, err := NewSchema(&fileSet, s.descriptor.GetName(), s.Version()+1)
	if err != nil {
		return nil, err // return nil if schema can't be built
	}
	return out, nil
}

// Helper method for retrieving the descriptor for the field value.
func (s *MutableSchema) extractDescriptor(fdp *descriptorpb.FieldDescriptorProto) schema.MutableDescriptor {
	d := s.extractDescriptorInternal(fdp)
	if fdp.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		// repeated means this is either an array or a map

		// protobuf internally represents maps as arrays of messages that
		// contain a key and a value, we need to find out if it's actually a map
		if sd, ok := d.(*MutableStructDescriptor); ok &&
			sd.descriptor.GetOptions().GetMapEntry() {
			d = &MutableMapDescriptor{
				schema:     s,
				descriptor: sd.descriptor,
			}
		} else {
			// it was not a map, so it is an array
			d = &MutableArrayDescriptor{
				schema:          s,
				descriptor:      fdp,
				valueDescriptor: d,
			}
		}
	}
	return d
}

// This method extracts the underlying descriptor. It does not repack array and
// map types, in both cases it just returns the descriptor for the underlying
// value.
func (s *MutableSchema) extractDescriptorInternal(fdp *descriptorpb.FieldDescriptorProto) schema.MutableDescriptor {
	switch fdp.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		// these have specific structs
	default:
		return &MutablePrimitiveDescriptor{
			schema:     s,
			descriptor: fdp,
		}
	}

	if !strings.HasPrefix(fdp.GetTypeName(), ".") {
		// TODO what do we do with these?
		panic(fmt.Sprintf("unsupported type name, expected a fully qualified name, got %q", fdp.GetTypeName()))
	}
	fullName := protoreflect.FullName(strings.TrimPrefix(fdp.GetTypeName(), "."))

	switch fdp.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:
		d := s.findDescriptorByName(fullName)
		return &MutableStructDescriptor{
			schema:     s,
			descriptor: d.(*descriptorpb.DescriptorProto),
		}
	case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		d := s.findDescriptorByName(fullName)
		return &MutableEnumDescriptor{
			schema:     s,
			descriptor: d.(*descriptorpb.EnumDescriptorProto),
		}
	default:
		panic(cerrors.Errorf("unhandled case: %q", fdp.GetType()))
	}
}

func (s *MutableSchema) findDescriptorByName(name protoreflect.FullName) interface{} {
	if name.Parent() == "" {
		if string(name) == s.descriptor.GetPackage() {
			return s.descriptor
		}
		// no such descriptor in the main file
		return nil
	}

	parentDescriptor := s.findDescriptorByName(name.Parent())
	if parentDescriptor == nil {
		// no such descriptor in the main file
		return nil
	}

	switch v := parentDescriptor.(type) {
	case *descriptorpb.FileDescriptorProto:
		// we are fetching a descriptor at the root of the file
		if d := s.findMessageDescriptor(s.descriptor.MessageType, name.Name()); d != nil {
			return d
		}
		if d := s.findEnumDescriptor(s.descriptor.EnumType, name.Name()); d != nil {
			return d
		}
	case *descriptorpb.DescriptorProto:
		if d := s.findMessageDescriptor(v.NestedType, name.Name()); d != nil {
			return d
		}
		if d := s.findEnumDescriptor(v.EnumType, name.Name()); d != nil {
			return d
		}
		if d := s.findFieldDescriptor(v.Field, name.Name()); d != nil {
			return d
		}
		// TODO oneof descriptor
	case *descriptorpb.EnumDescriptorProto:
		if d := s.findEnumValueDescriptor(v.Value, name.Name()); d != nil {
			return d
		}
	default:
		panic(fmt.Sprintf("unexpected descriptor type %T", parentDescriptor))
	}

	return nil
}

func (s *MutableSchema) findMessageDescriptor(descriptors []*descriptorpb.DescriptorProto, name protoreflect.Name) *descriptorpb.DescriptorProto {
	for _, d := range descriptors {
		if d.GetName() == string(name) {
			return d
		}
	}
	return nil
}
func (s *MutableSchema) findEnumDescriptor(descriptors []*descriptorpb.EnumDescriptorProto, name protoreflect.Name) *descriptorpb.EnumDescriptorProto {
	for _, d := range descriptors {
		if d.GetName() == string(name) {
			return d
		}
	}
	return nil
}
func (s *MutableSchema) findEnumValueDescriptor(descriptors []*descriptorpb.EnumValueDescriptorProto, name protoreflect.Name) *descriptorpb.EnumValueDescriptorProto {
	for _, d := range descriptors {
		if d.GetName() == string(name) {
			return d
		}
	}
	return nil
}
func (s *MutableSchema) findFieldDescriptor(descriptors []*descriptorpb.FieldDescriptorProto, name protoreflect.Name) *descriptorpb.FieldDescriptorProto {
	for _, d := range descriptors {
		if d.GetName() == string(name) {
			return d
		}
	}
	return nil
}

type MutableStructDescriptor struct {
	schema     *MutableSchema
	descriptor *descriptorpb.DescriptorProto

	fields []schema.MutableField
	once   sync.Once
}

func (d *MutableStructDescriptor) DescriptorType(schema.StructDescriptor) {}
func (d *MutableStructDescriptor) Name() string {
	return d.descriptor.GetName()
}
func (d *MutableStructDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutableStructDescriptor) SetName(s2 string) {
	d.schema.changed = true
	if d.descriptor.Name == nil {
		d.descriptor.Name = new(string)
	}
	*d.descriptor.Name = s2
	// TODO update references to this struct descriptor
}
func (d *MutableStructDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutableStructDescriptor) Fields() []schema.Field {
	d.once.Do(func() {
		fields := make([]schema.MutableField, len(d.descriptor.Field))
		for i, fdp := range d.descriptor.Field {
			fields[i] = &MutableField{
				schema:     d.schema,
				descriptor: fdp,
			}
		}
		d.fields = fields
	})

	// repack
	tmp := make([]schema.Field, len(d.fields))
	for i, v := range d.fields {
		tmp[i] = schema.Field(v)
	}
	return tmp
}
func (d *MutableStructDescriptor) SetFields(fields []schema.MutableField) {
	d.once.Do(func() { /* ensure we don't initialize fields anymore */ })
	d.schema.changed = true

	d.descriptor.Field = d.descriptor.Field[:0]
	for _, f := range fields {
		v, ok := f.(*MutableField)
		if !ok {
			panic(cerrors.Errorf("unexpected field type %T", f))
		}
		d.descriptor.Field = append(d.descriptor.Field, v.descriptor)
	}
	d.fields = fields
}

type MutableField struct {
	schema     *MutableSchema
	descriptor *descriptorpb.FieldDescriptorProto

	valueDescriptor schema.MutableDescriptor
	once            sync.Once
}

func NewMutableField(schema *MutableSchema, name string, index int, descriptor schema.MutableDescriptor) *MutableField {
	f := MutableField{
		schema:     schema,
		descriptor: new(descriptorpb.FieldDescriptorProto),
	}
	f.descriptor.Name = &name
	i := int32(index)
	f.descriptor.Number = &i
	f.SetDescriptor(descriptor)
	return &f
}

func (f *MutableField) DescriptorType(schema.Field) {}
func (f *MutableField) Name() string {
	return f.descriptor.GetName()
}
func (f *MutableField) Index() int {
	return int(f.descriptor.GetNumber()) // TODO should we use index instead?
}
func (f *MutableField) Descriptor() schema.Descriptor {
	f.once.Do(func() {
		d := f.schema.extractDescriptor(f.descriptor)
		f.valueDescriptor = d
	})
	return f.valueDescriptor
}
func (f *MutableField) SetName(s string) {
	f.schema.changed = true
	if f.descriptor.Name == nil {
		f.descriptor.Name = new(string)
	}
	*f.descriptor.Name = s
}
func (f *MutableField) SetIndex(i int) {
	f.schema.changed = true
	if f.descriptor.Number == nil {
		f.descriptor.Number = new(int32)
	}
	*f.descriptor.Number = int32(i)
}
func (f *MutableField) SetDescriptor(descriptor schema.MutableDescriptor) {
	f.once.Do(func() { /* ensure we don't initialize value descriptor anymore */ })
	f.schema.changed = true

	if f.descriptor.Type == nil {
		f.descriptor.Type = new(descriptorpb.FieldDescriptorProto_Type)
	}

	switch v := descriptor.(type) {
	case *MutableStructDescriptor:
		if f.descriptor.TypeName == nil {
			f.descriptor.TypeName = new(string)
		}
		*f.descriptor.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		*f.descriptor.TypeName = v.descriptor.GetName()
	case *MutableEnumDescriptor:
		if f.descriptor.TypeName == nil {
			f.descriptor.TypeName = new(string)
		}
		*f.descriptor.Type = descriptorpb.FieldDescriptorProto_TYPE_ENUM
		*f.descriptor.TypeName = v.descriptor.GetName()
	case *MutableMapDescriptor:
		// TODO not supported yet, we are not yet able to add a map message to the schema
		if f.descriptor.TypeName == nil {
			f.descriptor.TypeName = new(string)
		}
		*f.descriptor.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		*f.descriptor.TypeName = v.descriptor.GetName()
	case *MutableArrayDescriptor:
		f.SetDescriptor(v.valueDescriptor)
		*f.descriptor.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		v.descriptor = f.descriptor // TODO see top of the file
	case *MutablePrimitiveDescriptor:
		f.descriptor.TypeName = nil
		*f.descriptor.Type = *v.descriptor.Type
		v.descriptor = f.descriptor // TODO see top of the file
	default:
		panic(cerrors.Errorf("unexpected descriptor type %T", descriptor))
	}

	f.valueDescriptor = descriptor
}

type MutableMapDescriptor struct {
	schema     *MutableSchema
	descriptor *descriptorpb.DescriptorProto

	keyDescriptor   schema.MutableDescriptor
	valueDescriptor schema.MutableDescriptor
	keyOnce         sync.Once
	valueOnce       sync.Once
}

func (d *MutableMapDescriptor) DescriptorType(schema.MapDescriptor) {}
func (d *MutableMapDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutableMapDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutableMapDescriptor) KeyDescriptor() schema.Descriptor {
	d.keyOnce.Do(func() {
		d.keyDescriptor = d.schema.extractDescriptor(d.descriptor.Field[0])
	})
	return d.keyDescriptor
}
func (d *MutableMapDescriptor) ValueDescriptor() schema.Descriptor {
	d.valueOnce.Do(func() {
		d.valueDescriptor = d.schema.extractDescriptor(d.descriptor.Field[1])
	})
	return d.valueDescriptor
}
func (d *MutableMapDescriptor) SetKeyDescriptor(descriptor schema.MutableDescriptor) {
	d.keyOnce.Do(func() { /* ensure we don't initialize key descriptor anymore */ })
	d.schema.changed = true

	kd, ok := descriptor.(*MutablePrimitiveDescriptor)
	if !ok {
		panic(cerrors.Errorf("unexpected descriptor type %T", descriptor))
	}
	*d.descriptor.Field[0].Type = *kd.descriptor.Type

	d.keyDescriptor = descriptor
	kd.descriptor = d.descriptor.Field[0] // TODO see top of the file
}
func (d *MutableMapDescriptor) SetValueDescriptor(descriptor schema.MutableDescriptor) {
	d.valueOnce.Do(func() { /* ensure we don't initialize value descriptor anymore */ })
	d.schema.changed = true

	fd := d.descriptor.Field[1]

	switch v := descriptor.(type) {
	case *MutableStructDescriptor:
		if fd.TypeName == nil {
			fd.TypeName = new(string)
		}
		*fd.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		*fd.TypeName = v.descriptor.GetName()
	case *MutableEnumDescriptor:
		if fd.TypeName == nil {
			fd.TypeName = new(string)
		}
		*fd.Type = descriptorpb.FieldDescriptorProto_TYPE_ENUM
		*fd.TypeName = v.descriptor.GetName()
	case *MutablePrimitiveDescriptor:
		fd.TypeName = nil
		*fd.Type = *v.descriptor.Type
		v.descriptor = fd // TODO see top of the file
	default:
		panic(cerrors.Errorf("unexpected descriptor type %T", descriptor))
	}

	d.valueDescriptor = descriptor
}

type MutableArrayDescriptor struct {
	schema          *MutableSchema
	descriptor      *descriptorpb.FieldDescriptorProto
	valueDescriptor schema.MutableDescriptor
}

func NewMutableArrayDescriptor(schema *MutableSchema, descriptor schema.MutableDescriptor) *MutableArrayDescriptor {
	d := MutableArrayDescriptor{
		schema:     schema,
		descriptor: new(descriptorpb.FieldDescriptorProto),
	}
	d.descriptor.Type = new(descriptorpb.FieldDescriptorProto_Type)
	d.SetValueDescriptor(descriptor)
	return &d
}

func (d *MutableArrayDescriptor) DescriptorType(schema.ArrayDescriptor) {}
func (d *MutableArrayDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutableArrayDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutableArrayDescriptor) ValueDescriptor() schema.Descriptor {
	return d.valueDescriptor
}
func (d *MutableArrayDescriptor) SetValueDescriptor(descriptor schema.MutableDescriptor) {
	d.schema.changed = true

	switch v := descriptor.(type) {
	case *MutableStructDescriptor:
		if d.descriptor.TypeName == nil {
			d.descriptor.TypeName = new(string)
		}
		*d.descriptor.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		*d.descriptor.TypeName = v.descriptor.GetName()
	case *MutableEnumDescriptor:
		if d.descriptor.TypeName == nil {
			d.descriptor.TypeName = new(string)
		}
		*d.descriptor.Type = descriptorpb.FieldDescriptorProto_TYPE_ENUM
		*d.descriptor.TypeName = v.descriptor.GetName()
	case *MutablePrimitiveDescriptor:
		d.descriptor.TypeName = nil
		*d.descriptor.Type = *v.descriptor.Type
		v.descriptor = d.descriptor // TODO see top of the file
	default:
		panic(cerrors.Errorf("unexpected descriptor type %T", descriptor))
	}

	d.valueDescriptor = descriptor
}

type MutableEnumDescriptor struct {
	schema     *MutableSchema
	descriptor *descriptorpb.EnumDescriptorProto

	valueDescriptors []schema.MutableEnumValueDescriptor
	once             sync.Once
}

func (d *MutableEnumDescriptor) DescriptorType(schema.EnumDescriptor) {}
func (d *MutableEnumDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutableEnumDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutableEnumDescriptor) Name() string {
	return d.descriptor.GetName()
}
func (d *MutableEnumDescriptor) SetName(name string) {
	d.schema.changed = true
	if d.descriptor.Name == nil {
		d.descriptor.Name = new(string)
	}
	*d.descriptor.Name = name
}
func (d *MutableEnumDescriptor) ValueDescriptors() []schema.EnumValueDescriptor {
	d.once.Do(func() {
		valueDescriptors := make([]schema.MutableEnumValueDescriptor, len(d.descriptor.Value))
		for i, evd := range d.descriptor.Value {
			valueDescriptors[i] = &MutableEnumValueDescriptor{
				schema:     d.schema,
				descriptor: evd,
			}
		}
		d.valueDescriptors = valueDescriptors
	})

	// repack
	tmp := make([]schema.EnumValueDescriptor, len(d.valueDescriptors))
	for i, v := range d.valueDescriptors {
		tmp[i] = v
	}
	return tmp
}

func (d *MutableEnumDescriptor) SetValueDescriptors(descriptors []schema.MutableEnumValueDescriptor) {
	d.once.Do(func() { /* ensure we don't initialize value descriptors anymore */ })
	d.schema.changed = true

	d.descriptor.Value = d.descriptor.Value[:0]
	for _, f := range descriptors {
		v, ok := f.(*MutableEnumValueDescriptor)
		if !ok {
			panic(cerrors.Errorf("unexpected field type %T", f))
		}
		d.descriptor.Value = append(d.descriptor.Value, v.descriptor)
	}
	d.valueDescriptors = descriptors
}

type MutableEnumValueDescriptor struct {
	schema     *MutableSchema
	descriptor *descriptorpb.EnumValueDescriptorProto
}

func NewMutableEnumValueDescriptor(s *MutableSchema, name string, value int) *MutableEnumValueDescriptor {
	d := MutableEnumValueDescriptor{
		schema:     s,
		descriptor: new(descriptorpb.EnumValueDescriptorProto),
	}
	d.descriptor.Name = &name
	i := int32(value)
	d.descriptor.Number = &i
	return &d
}

func (d *MutableEnumValueDescriptor) DescriptorType(schema.EnumValueDescriptor) {}
func (d *MutableEnumValueDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutableEnumValueDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutableEnumValueDescriptor) Name() string {
	return d.descriptor.GetName()
}
func (d *MutableEnumValueDescriptor) SetName(name string) {
	d.schema.changed = true
	if d.descriptor.Name == nil {
		d.descriptor.Name = new(string)
	}
	*d.descriptor.Name = name
}
func (d *MutableEnumValueDescriptor) Value() string {
	return strconv.FormatInt(int64(d.descriptor.GetNumber()), 10)
}
func (d *MutableEnumValueDescriptor) SetValue(value string) {
	d.schema.changed = true

	i, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		panic(cerrors.Errorf("unexpected enum value (only 32-bit integers are supported): %w", err))
	}
	*d.descriptor.Number = int32(i)
}

type MutablePrimitiveDescriptor struct {
	schema     *MutableSchema
	descriptor *descriptorpb.FieldDescriptorProto
}

func NewMutablePrimitiveDescriptor(schema *MutableSchema, descriptorType schema.PrimitiveDescriptorType) *MutablePrimitiveDescriptor {
	d := MutablePrimitiveDescriptor{
		schema:     schema,
		descriptor: new(descriptorpb.FieldDescriptorProto),
	}
	d.descriptor.Type = new(descriptorpb.FieldDescriptorProto_Type)
	*d.descriptor.Type = d.schemaTypeToProtoType(descriptorType)
	return &d
}

func (d *MutablePrimitiveDescriptor) DescriptorType(schema.PrimitiveDescriptor) {}
func (d *MutablePrimitiveDescriptor) String() string {
	return fmt.Sprintf("%s (%s)", d.Type(), d.descriptor.GetType())
}
func (d *MutablePrimitiveDescriptor) Parameters() map[string]interface{} {
	return map[string]interface{}{
		// TODO add parameters
	}
}
func (d *MutablePrimitiveDescriptor) SetParameters(params map[string]interface{}) {
	d.schema.changed = true
	// TODO parse parameters
}
func (d *MutablePrimitiveDescriptor) Type() schema.PrimitiveDescriptorType {
	return d.protoTypeToSchemaType(d.descriptor.GetType())
}

func (d *MutablePrimitiveDescriptor) SetType(descriptorType schema.PrimitiveDescriptorType) {
	d.schema.changed = true
	*d.descriptor.Type = d.schemaTypeToProtoType(descriptorType)
}

func (d *MutablePrimitiveDescriptor) schemaTypeToProtoType(descriptorType schema.PrimitiveDescriptorType) descriptorpb.FieldDescriptorProto_Type {
	typeMapping := map[schema.PrimitiveDescriptorType]descriptorpb.FieldDescriptorProto_Type{
		schema.Boolean: descriptorpb.FieldDescriptorProto_TYPE_BOOL,
		schema.String:  descriptorpb.FieldDescriptorProto_TYPE_STRING,
		schema.Bytes:   descriptorpb.FieldDescriptorProto_TYPE_BYTES,

		schema.Float32: descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
		schema.Float64: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE,

		schema.Int32:  descriptorpb.FieldDescriptorProto_TYPE_INT32,
		schema.Int64:  descriptorpb.FieldDescriptorProto_TYPE_INT64,
		schema.UInt32: descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		schema.UInt64: descriptorpb.FieldDescriptorProto_TYPE_UINT64,

		schema.Unknown: 0, // TODO what to do with unknown types? panic?
	}
	return typeMapping[descriptorType]
}

func (d *MutablePrimitiveDescriptor) protoTypeToSchemaType(descriptorType descriptorpb.FieldDescriptorProto_Type) schema.PrimitiveDescriptorType {
	typeMapping := map[descriptorpb.FieldDescriptorProto_Type]schema.PrimitiveDescriptorType{
		descriptorpb.FieldDescriptorProto_TYPE_BOOL:   schema.Boolean,
		descriptorpb.FieldDescriptorProto_TYPE_STRING: schema.String,
		descriptorpb.FieldDescriptorProto_TYPE_BYTES:  schema.Bytes,

		descriptorpb.FieldDescriptorProto_TYPE_FLOAT:  schema.Float32,
		descriptorpb.FieldDescriptorProto_TYPE_DOUBLE: schema.Float64,

		descriptorpb.FieldDescriptorProto_TYPE_INT32:    schema.Int32,
		descriptorpb.FieldDescriptorProto_TYPE_INT64:    schema.Int64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32:   schema.Int32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64:   schema.Int64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32: schema.Int32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64: schema.Int64,

		descriptorpb.FieldDescriptorProto_TYPE_UINT32:  schema.UInt32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64:  schema.UInt64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32: schema.UInt32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64: schema.UInt64,

		// no support for group types
		descriptorpb.FieldDescriptorProto_TYPE_GROUP: schema.Unknown,
	}
	return typeMapping[descriptorType]
}
