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
	"io/ioutil"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	standaloneDescriptorSetPath = "data/standalone.desc"
	test1DescriptorSetPath      = "data/test1.desc"
)

func getFileDescriptorSet(t *testing.T, path string) *descriptorpb.FileDescriptorSet {
	f, err := os.Open(path)
	assert.Ok(t, err)

	content, err := ioutil.ReadAll(f)
	assert.Ok(t, err)

	var fds descriptorpb.FileDescriptorSet
	err = proto.Unmarshal(content, &fds)
	assert.Ok(t, err)

	return &fds
}

func TestNewSchema(t *testing.T) {
	testCases := []struct {
		path           string
		mainDescriptor string
		wantErr        error
	}{{
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "",
		wantErr:        nil,
		// TODO uncomment test case once we support imports
		// }, {
		//	path:           test1DescriptorSetPath,
		//	mainDescriptor: "",
		//	wantErr:        cerrors.New("missing main descriptor path"),
	}, {
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "standalone.proto",
		wantErr:        nil,
	}, {
		path:           test1DescriptorSetPath,
		mainDescriptor: "test1.proto",
		wantErr:        nil,
	}, {
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "test1.proto",
		wantErr:        cerrors.New(`could not find main descriptor "test1.proto": proto: not found`),
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)

			_, err := NewSchema(fds, tc.mainDescriptor, 1)
			assertError(t, tc.wantErr, err)
		})
	}
}

// TestSchema runs the generic acceptance test.
func TestSchema(t *testing.T) {
	testCases := []struct {
		path           string
		mainDescriptor string
		wantErr        error
	}{{
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "",
		wantErr:        nil,
	}, {
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		wantErr:        nil,
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)

			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)
			schema.AcceptanceTest(t, s)
		})
	}
}

func TestSchema_Type(t *testing.T) {
	fds := getFileDescriptorSet(t, standaloneDescriptorSetPath)

	s, err := NewSchema(fds, "", 1)
	assert.Ok(t, err)
	assert.Equal(t, SchemaType, s.Type())
}

func TestSchema_Version(t *testing.T) {
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
			fds := getFileDescriptorSet(t, standaloneDescriptorSetPath)

			s, err := NewSchema(fds, "", tc.version)
			assertError(t, tc.wantErr, err)
			assert.Equal(t, tc.version, s.Version())
		})
	}
}

func TestSchema_Descriptors(t *testing.T) {
	testCases := []struct {
		path              string
		mainDescriptor    string
		assertDescriptors func(*testing.T, []schema.Descriptor)
	}{{
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "",
		assertDescriptors: func(t *testing.T, descriptors []schema.Descriptor) {
			assert.Equal(t, 1, len(descriptors))

			sd, ok := descriptors[0].(StructDescriptor)
			assert.Equal(t, true, ok)
			assert.Equal(t, "Foo", sd.Name())
		},
	}, {
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		assertDescriptors: func(t *testing.T, descriptors []schema.Descriptor) {
			assert.Equal(t, 6, len(descriptors))

			d1, ok := descriptors[0].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptors[0])
			assert.Equal(t, "Foo", d1.Name())

			d2, ok := descriptors[1].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptors[1])
			assert.Equal(t, "AllTypes", d2.Name())

			d3, ok := descriptors[2].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptors[2])
			assert.Equal(t, "Empty", d3.Name())

			d4, ok := descriptors[3].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptors[3])
			assert.Equal(t, "Nested", d4.Name())

			d5, ok := descriptors[4].(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", EnumDescriptor{}, descriptors[4])
			assert.Equal(t, "MyEnum", d5.Name())

			d6, ok := descriptors[5].(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", EnumDescriptor{}, descriptors[5])
			assert.Equal(t, "UnusedEnum", d6.Name())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			tc.assertDescriptors(t, s.Descriptors())
		})
	}
}

func TestStructDescriptor_Fields(t *testing.T) {
	testCases := []struct {
		path             string
		mainDescriptor   string
		getDescriptor    func(*testing.T, schema.Schema) StructDescriptor
		assertDescriptor func(*testing.T, StructDescriptor)
	}{{
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor: func(t *testing.T, s schema.Schema) StructDescriptor {
			d, ok := s.Descriptors()[1].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, s.Descriptors()[1])
			assert.Equal(t, "AllTypes", d.Name())
			return d
		},
		assertDescriptor: func(t *testing.T, descriptor StructDescriptor) {
			fields := descriptor.Fields()

			assert.Equal(t, 19, len(fields))
			for i, f := range fields {
				assert.Equal(t, fmt.Sprintf("f%d", i+1), f.Name())

				if i < 19 {
					assert.Equal(t, i+1, f.Index())
				} else {
					assert.Equal(t, (i+1)*10, f.Index())
				}

				switch i {
				case 15:
					_, ok := f.Descriptor().(StructDescriptor)
					assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, f.Descriptor())
				case 16:
					_, ok := f.Descriptor().(ArrayDescriptor)
					assert.True(t, ok, "expected %T, got %T", ArrayDescriptor{}, f.Descriptor())
				case 17:
					_, ok := f.Descriptor().(MapDescriptor)
					assert.True(t, ok, "expected %T, got %T", MapDescriptor{}, f.Descriptor())
				case 18:
					_, ok := f.Descriptor().(EnumDescriptor)
					assert.True(t, ok, "expected %T, got %T", EnumDescriptor{}, f.Descriptor())

				default:
					// first 15 fields should be primitive types
					_, ok := f.Descriptor().(PrimitiveDescriptor)
					assert.True(t, ok, "expected %T, got %T", PrimitiveDescriptor{}, f.Descriptor())
				}
			}
		},
	}, {
		path:           standaloneDescriptorSetPath,
		mainDescriptor: "",
		getDescriptor: func(t *testing.T, s schema.Schema) StructDescriptor {
			d, ok := s.Descriptors()[0].(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, s.Descriptors()[0])
			assert.Equal(t, "Foo", d.Name())
			return d
		},
		assertDescriptor: func(t *testing.T, descriptor StructDescriptor) {
			fields := descriptor.Fields()

			assert.Equal(t, 2, len(fields))

			assert.Equal(t, "key", fields[0].Name())
			assert.Equal(t, 1, fields[0].Index())
			d1, ok := fields[0].Descriptor().(PrimitiveDescriptor)
			assert.Equal(t, true, ok)
			assert.Equal(t, schema.String, d1.Type())

			assert.Equal(t, "value", fields[1].Name())
			assert.Equal(t, 2, fields[1].Index())
			d2, ok := fields[1].Descriptor().(PrimitiveDescriptor)
			assert.Equal(t, true, ok)
			assert.Equal(t, schema.String, d2.Type())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			sd := tc.getDescriptor(t, s)
			tc.assertDescriptor(t, sd)
		})
	}
}

func TestArrayDescriptor(t *testing.T) {
	testCases := []struct {
		path             string
		mainDescriptor   string
		getDescriptor    func(*testing.T, schema.Schema) ArrayDescriptor
		assertDescriptor func(*testing.T, ArrayDescriptor)
	}{{
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor: func(t *testing.T, s schema.Schema) ArrayDescriptor {
			// get descriptor for AllTypes.f17, it is an array
			d, ok := s.Descriptors()[1].(StructDescriptor).Fields()[16].Descriptor().(ArrayDescriptor)
			assert.True(t, ok, "expected %T, got %T", ArrayDescriptor{}, s.Descriptors()[1].(StructDescriptor).Fields()[16].Descriptor())
			return d
		},
		assertDescriptor: func(t *testing.T, descriptor ArrayDescriptor) {
			d, ok := descriptor.ValueDescriptor().(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptor.ValueDescriptor())
			assert.Equal(t, "Foo", d.Name())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			sd := tc.getDescriptor(t, s)
			tc.assertDescriptor(t, sd)
		})
	}
}

func TestMapDescriptor(t *testing.T) {
	testCases := []struct {
		path             string
		mainDescriptor   string
		getDescriptor    func(*testing.T, schema.Schema) MapDescriptor
		assertDescriptor func(*testing.T, MapDescriptor)
	}{{
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor: func(t *testing.T, s schema.Schema) MapDescriptor {
			// get descriptor for AllTypes.f18, it is a map
			d, ok := s.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor().(MapDescriptor)
			assert.True(t, ok, "expected %T, got %T", MapDescriptor{}, s.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor())
			return d
		},
		assertDescriptor: func(t *testing.T, descriptor MapDescriptor) {
			d1, ok := descriptor.KeyDescriptor().(PrimitiveDescriptor)
			assert.True(t, ok, "expected %T, got %T", PrimitiveDescriptor{}, descriptor.KeyDescriptor())
			assert.Equal(t, schema.String, d1.Type())

			d2, ok := descriptor.ValueDescriptor().(StructDescriptor)
			assert.True(t, ok, "expected %T, got %T", StructDescriptor{}, descriptor.ValueDescriptor())
			assert.Equal(t, "Foo", d2.Name())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			sd := tc.getDescriptor(t, s)
			tc.assertDescriptor(t, sd)
		})
	}
}

func TestEnumDescriptor(t *testing.T) {
	testCases := []struct {
		path             string
		mainDescriptor   string
		getDescriptor    func(*testing.T, schema.Schema) EnumDescriptor
		assertDescriptor func(*testing.T, EnumDescriptor)
	}{{
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor: func(t *testing.T, s schema.Schema) EnumDescriptor {
			// get descriptor for AllTypes.f19, it is an enum
			d, ok := s.Descriptors()[1].(StructDescriptor).Fields()[18].Descriptor().(EnumDescriptor)
			assert.True(t, ok, "expected %T, got %T", EnumDescriptor{}, s.Descriptors()[1].(StructDescriptor).Fields()[18].Descriptor())
			return d
		},
		assertDescriptor: func(t *testing.T, descriptor EnumDescriptor) {
			assert.Equal(t, "MyEnum", descriptor.Name())
			assert.Equal(t, 3, len(descriptor.ValueDescriptors()))

			vd1 := descriptor.ValueDescriptors()[0]
			assert.Equal(t, "Val0", vd1.Name())
			assert.Equal(t, "0", vd1.Value())

			vd2 := descriptor.ValueDescriptors()[1]
			assert.Equal(t, "Val1", vd2.Name())
			assert.Equal(t, "1", vd2.Value())

			vd3 := descriptor.ValueDescriptors()[2]
			assert.Equal(t, "Val5", vd3.Name())
			assert.Equal(t, "5", vd3.Value())
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			sd := tc.getDescriptor(t, s)
			tc.assertDescriptor(t, sd)
		})
	}
}

func TestReusedDescriptors(t *testing.T) {
	testCases := []struct {
		path           string
		mainDescriptor string
		getDescriptor1 func(*testing.T, schema.Schema) schema.Descriptor
		getDescriptor2 func(*testing.T, schema.Schema) schema.Descriptor
	}{{
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor1: func(t *testing.T, s schema.Schema) schema.Descriptor {
			// MyEnum
			return s.Descriptors()[4].(EnumDescriptor)
		},
		getDescriptor2: func(t *testing.T, s schema.Schema) schema.Descriptor {
			// AllTypes.f19
			return s.Descriptors()[1].(StructDescriptor).Fields()[18].Descriptor().(EnumDescriptor)
		},
	}, {
		path:           test1DescriptorSetPath,
		mainDescriptor: "",
		getDescriptor1: func(t *testing.T, s schema.Schema) schema.Descriptor {
			// Foo
			return s.Descriptors()[0]
		},
		getDescriptor2: func(t *testing.T, s schema.Schema) schema.Descriptor {
			// AllTypes.f18 (value of map)
			return s.Descriptors()[1].(StructDescriptor).Fields()[17].Descriptor().(MapDescriptor).ValueDescriptor()
		},
	}}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			fds := getFileDescriptorSet(t, tc.path)
			s, err := NewSchema(fds, tc.mainDescriptor, 1)
			assert.Ok(t, err)

			d1 := tc.getDescriptor1(t, s)
			d2 := tc.getDescriptor2(t, s)
			assert.Equal(t, d1, d2)
		})
	}
}

// assertError fails if the errors do not match.
func assertError(tb testing.TB, want error, got error) {
	//nolint:gocritic // no single value to have a switch on
	if want == nil {
		assert.Ok(tb, got)
	} else if got == nil {
		assert.Equal(tb, want.Error(), got)
	} else {
		// sanitize error string, protobuf randomly adds a non-breaking space
		errStr := strings.ReplaceAll(got.Error(), "\u00a0", " ")
		assert.Equal(tb, want.Error(), errStr)
	}
}
