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

package schema

import (
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
)

// AcceptanceTest is the acceptance test that all implementations of Schema
// should pass. It should manually be called from a test case in each
// implementation:
//
//	func TestSchema(t *testing.T) {
//	    s = NewSchema()
//	    schema.AcceptanceTest(t, s)
//	}
func AcceptanceTest(t *testing.T, schema Schema) {
	testMutableSchemaSameAsSchema(t, schema)
}

// Tests that converting Schema to MutableSchema preserves the descriptors.
func testMutableSchemaSameAsSchema(t *testing.T, s Schema) {
	t.Run(testName(), func(t *testing.T) {
		ms := s.ToMutable()
		assert.Equal(t, s.Type(), ms.Type())
		assert.Equal(t, s.Version(), ms.Version())

		d1 := s.Descriptors()
		d2 := ms.Descriptors()
		assert.Equal(t, len(d1), len(d2))
		for i := 0; i < len(d1); i++ {
			assertDescriptorsEqual(t, d1[i], d2[i])
		}
	})
}

func assertDescriptorsEqual(tb testing.TB, d1 Descriptor, d2 Descriptor) {
	assert.Equal(tb, d1.Parameters(), d2.Parameters())

	switch v1 := d1.(type) {
	case StructDescriptor:
		v2, ok := d2.(StructDescriptor)
		assert.True(tb, ok, "expected %T, got %T", d1, d2)

		assert.Equal(tb, v1.Name(), v2.Name())

		f1 := v1.Fields()
		f2 := v2.Fields()
		assert.Equal(tb, len(f1), len(f2))
		for i := 0; i < len(f1); i++ {
			assertFieldsEqual(tb, f1[i], f2[i])
		}
	case EnumDescriptor:
		v2, ok := d2.(EnumDescriptor)
		assert.True(tb, ok, "expected %T, got %T", d1, d2)

		assert.Equal(tb, v1.Name(), v2.Name())

		vd1 := v1.ValueDescriptors()
		vd2 := v2.ValueDescriptors()
		assert.Equal(tb, len(vd1), len(vd2))
		for i := 0; i < len(vd1); i++ {
			assertDescriptorsEqual(tb, vd1[i], vd2[i])
		}
	case EnumValueDescriptor:
		v2, ok := d2.(EnumValueDescriptor)
		assert.True(tb, ok, "expected %T, got %T", d1, d2)

		assert.Equal(tb, v1.Name(), v2.Name())
		assert.Equal(tb, v1.Value(), v2.Value())
	case MapDescriptor:
		v2, ok := d2.(MapDescriptor)
		assert.True(tb, ok, "expected %T, got %T", d1, d2)

		assertDescriptorsEqual(tb, v1.KeyDescriptor(), v2.KeyDescriptor())
		assertDescriptorsEqual(tb, v1.ValueDescriptor(), v2.ValueDescriptor())
	case ArrayDescriptor:
		v2, ok := d2.(ArrayDescriptor)
		assert.True(tb, ok, "expected %T, got %T", d1, d2)

		assertDescriptorsEqual(tb, v1.ValueDescriptor(), v2.ValueDescriptor())
	}
}

func assertFieldsEqual(tb testing.TB, f1 Field, f2 Field) {
	assert.Equal(tb, f1.Name(), f2.Name())
	assert.Equal(tb, f1.Index(), f2.Index())
	assertDescriptorsEqual(tb, f1.Descriptor(), f2.Descriptor())
}

// testName returns the name of the acceptance test (function name).
func testName() string {
	//nolint:dogsled // not important in tests
	pc, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(pc).Name()
	return caller[strings.LastIndex(caller, ".")+1:]
}
