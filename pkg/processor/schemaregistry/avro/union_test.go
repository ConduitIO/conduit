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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestUnionResolver(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name            string
		have            any
		want            any
		skipNestedArray bool
	}{{
		name: "string",
		have: "foo",
		want: map[string]any{"string": "foo"},
	}, {
		name: "int",
		have: 123,
		want: map[string]any{"int": 123},
	}, {
		name: "boolean",
		have: true,
		want: map[string]any{"boolean": true},
	}, {
		name: "double",
		have: 1.23,
		want: map[string]any{"double": 1.23},
	}, {
		name: "float",
		have: float32(1.23),
		want: map[string]any{"float": float32(1.23)},
	}, {
		name: "long",
		have: int64(321),
		want: map[string]any{"long": int64(321)},
	}, {
		name: "bytes",
		have: []byte{1, 2, 3, 4},
		want: map[string]any{"bytes": []byte{1, 2, 3, 4}},
	}, {
		name: "null",
		have: nil,
		want: nil,
	}, {
		name:            "int array",
		have:            []int{1, 2, 3, 4},
		want:            map[string]any{"array": []int{1, 2, 3, 4}},
		skipNestedArray: true, // nested arrays don't work yet, see TODO in reflect.go
	}, {
		name:            "nil bool array",
		have:            []bool(nil),
		want:            map[string]any{"array": []bool(nil)},
		skipNestedArray: true, // nested arrays don't work yet, see TODO in reflect.go
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			newRecord := func() record.StructuredData {
				sd := record.StructuredData{
					"foo1": tc.have,
					"map1": map[string]any{
						"foo2": tc.have,
						"map2": map[string]any{
							"foo3": tc.have,
						},
					},
					"arr1": []any{
						tc.have,
						[]any{tc.have},
					},
				}
				if tc.skipNestedArray {
					sd["arr1"] = sd["arr1"].([]any)[:1] // remove nested array
				}
				return sd
			}
			want := record.StructuredData{
				"foo1": tc.have, // normal field shouldn't change
				"map1": map[string]any{
					"foo2": tc.want,
					"map2": map[string]any{
						"map": map[string]any{
							"foo3": tc.want,
						},
					},
				},
				"arr1": []any{
					tc.want,
					map[string]any{
						"array": []any{tc.want},
					},
				},
			}
			if tc.skipNestedArray {
				want["arr1"] = want["arr1"].([]any)[:1] // remove nested array
			}
			have := newRecord()

			schema, err := SchemaForType(have)
			is.NoErr(err)
			mur := NewUnionResolver(schema.schema)

			// before marshal we should change the nested map
			err = mur.BeforeMarshal(have)
			is.NoErr(err)
			is.Equal(cmp.Diff(want, have), "")

			// after unmarshal we should have the same record as at the start
			err = mur.AfterUnmarshal(have)
			is.NoErr(err)
			is.Equal(newRecord(), have)
		})
	}
}
