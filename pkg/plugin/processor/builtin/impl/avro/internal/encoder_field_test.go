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

package internal

import (
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
)

// TestFieldFromCodecPath covers the branches TestFromRecord_Mismatch does not
// reach through a real encode: a nested path, an index, a path that names no
// key of the record, and a message with no field marker at all. The name is
// the only thing taken from a codec message, because the rest can quote record
// values.
func TestFieldFromCodecPath(t *testing.T) {
	sd := opencdc.StructuredData{"c00_int": 1, "c02_numeric": 2, "nested": map[string]any{}}

	testCases := []struct {
		name   string
		msg    string
		want   string
		wantOK bool
	}{{
		name:   "top-level field",
		msg:    `could not marshal into avro: avro decimal value is not representable in its schema: field "c02_numeric": the value needs precision 4 and scale 3`,
		want:   "c02_numeric",
		wantOK: true,
	}, {
		name:   "nested path keeps the top level",
		msg:    `field "nested.inner": the value needs precision 4`,
		want:   "nested",
		wantOK: true,
	}, {
		name:   "indexed path keeps the top level",
		msg:    `field "nested[0]": the value needs precision 4`,
		want:   "nested",
		wantOK: true,
	}, {
		// <root> is what conduit-commons formats for an empty path; it is not
		// a key, so the caller keeps its "(record)" fallback.
		name: "root path is not a key",
		msg:  `field "<root>": the value needs precision 4`,
	}, {
		name: "path names no key of the record",
		msg:  `field "not_a_column": the value needs precision 4`,
	}, {
		name: "no field marker",
		msg:  "could not marshal into avro: avro: unknown type",
	}, {
		name: "unterminated quote",
		msg:  `field "c00_int`,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fieldFromCodecPath(sd, tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got field %q)", ok, tc.wantOK, got)
			}
			if got != tc.want {
				t.Fatalf("field = %q, want %q", got, tc.want)
			}
		})
	}
}
