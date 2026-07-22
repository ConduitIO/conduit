// Copyright © 2026 Meroxa, Inc.
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

package index_test

import (
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestCanonicalize_KnownVectors pins RFC 8785 (JCS) behavior against known,
// independently-verified vectors (plan-v2 §15.1: "JCS determinism against
// known vectors") — key sorting, whitespace removal, and number
// normalization are exactly the properties a signature's integrity depends
// on, so this is not a superficial formatting test.
func TestCanonicalize_KnownVectors(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sorts_top_level_keys", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"strips_whitespace", `{ "a" : 1 , "b" : 2 }`, `{"a":1,"b":2}`},
		{"normalizes_float_to_int", `{"a":1.0}`, `{"a":1}`},
		{"normalizes_exponent", `{"a":10E3}`, `{"a":10000}`},
		{"sorts_nested_keys", `{"z":1,"a":{"y":2,"b":1}}`, `{"a":{"b":1,"y":2},"z":1}`},
		{"preserves_array_order", `[3,2,1]`, `[3,2,1]`},
		{"preserves_unicode", `{"€":"euro"}`, `{"€":"euro"}`},
		{"preserves_literals", `{"a":true,"b":false,"c":null}`, `{"a":true,"b":false,"c":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			got, err := index.Canonicalize([]byte(tt.in))
			is.NoErr(err)
			is.Equal(string(got), tt.want)
		})
	}

	// Two inputs that differ only in key order/whitespace/number spelling
	// must canonicalize to byte-identical output — this is the actual
	// property a shared signature verification depends on.
	a, err := index.Canonicalize([]byte(`{"b": 2, "a": 1.0}`))
	is.NoErr(err)
	b, err := index.Canonicalize([]byte(`{ "a":1,"b":2.0 }`))
	is.NoErr(err)
	is.Equal(string(a), string(b))
}

func TestCanonicalize_InvalidJSON(t *testing.T) {
	is := is.New(t)
	_, err := index.Canonicalize([]byte(`{not valid`))
	is.True(err != nil)
}
