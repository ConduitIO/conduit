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

//go:generate mockgen -destination=mock/mutable.go -package=mock -mock_names=MutableSchema=MutableSchema,MutableStructDescriptor=MutableStructDescriptor,MutableField=MutableField,MutableMapDescriptor=MutableMapDescriptor,MutableArrayDescriptor=MutableArrayDescriptor,MutablePrimitiveDescriptor=MutablePrimitiveDescriptor,MutableEnumDescriptor=MutableEnumDescriptor,MutableEnumValueDescriptor=MutableEnumValueDescriptor . MutableSchema,MutableStructDescriptor,MutableField,MutableMapDescriptor,MutableArrayDescriptor,MutablePrimitiveDescriptor,MutableEnumDescriptor,MutableEnumValueDescriptor

package schema

// MutableSchema provides functionality for changing the underlying descriptors
// or the version of a schema and essentially building a new schema.
type MutableSchema interface {
	// Type returns the schema type (e.g. protobuf).
	Type() string
	// Version represents the schema version. A higher version represents a
	// newer schema.
	Version() int
	// Descriptors returns descriptors defined at the root of this schema.
	Descriptors() []Descriptor

	// SetVersion sets the version.
	SetVersion(int)
	// SetDescriptors sets the descriptors defined at the root of this schema.
	// Any descriptors, that were defined before, are overwritten.
	SetDescriptors([]MutableDescriptor)

	// Build validates the schema, compiles it and returns an immutable Schema.
	// If the schema can't be validated or compiled it returns an error.
	Build() (Schema, error)
}

// MutableDescriptor is a mutable instance of a Descriptor. Any
// MutableDescriptor has to implement at least one of the following interfaces:
//  - MutableStructDescriptor
//  - MutableMapDescriptor
//  - MutableArrayDescriptor
//  - MutablePrimitiveDescriptor
//  - MutableEnumDescriptor
//  - MutableEnumValueDescriptor
type MutableDescriptor interface {
	Descriptor
	// SetParameters sets additional settings specific for the schema type.
	SetParameters(map[string]interface{})
}

// MutableStructDescriptor is a mutable instance of a StructDescriptor.
type MutableStructDescriptor interface {
	StructDescriptor
	SetName(string)
	SetFields([]MutableField)
}

// MutableField is a mutable instance of a Field.
type MutableField interface {
	Field
	SetName(string)
	SetIndex(int)
	SetDescriptor(MutableDescriptor)
}

// MutableMapDescriptor is a mutable instance of a MapDescriptor.
type MutableMapDescriptor interface {
	MapDescriptor
	SetKeyDescriptor(MutableDescriptor)
	SetValueDescriptor(MutableDescriptor)
}

// MutableArrayDescriptor is a mutable instance of a ArrayDescriptor.
type MutableArrayDescriptor interface {
	ArrayDescriptor
	SetValueDescriptor(MutableDescriptor)
}

// MutablePrimitiveDescriptor is a mutable instance of a PrimitiveDescriptor.
type MutablePrimitiveDescriptor interface {
	PrimitiveDescriptor
	SetType(PrimitiveDescriptorType)
}

// MutableEnumDescriptor is a mutable instance of a EnumDescriptor.
type MutableEnumDescriptor interface {
	EnumDescriptor
	SetName(string)
	SetValueDescriptors([]MutableEnumValueDescriptor)
}

// MutableEnumValueDescriptor is a mutable instance of a EnumValueDescriptor.
type MutableEnumValueDescriptor interface {
	EnumValueDescriptor
	SetName(string)
	SetValue(string)
}
