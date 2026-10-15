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

package connect

import (
	"math/big"
	"testing"

	"github.com/matryer/is"
)

func TestSimpleHeaderConverter(t *testing.T) {
	is := is.New(t)
	key := &Schema{Type: TypeStruct, Fields: []Field{
		{Name: "seq", Schema: &Schema{Type: TypeInt32}},
		{Name: "region", Schema: &Schema{Type: TypeString}},
		{Name: "q", Schema: &Schema{Type: TypeString, Optional: true}},
	}}
	v, err := FromGo(key, map[string]any{"seq": 2, "region": `u"s\`, "q": nil})
	is.NoErr(err)
	got, err := SimpleHeaderConverter{}.Marshal(key, v)
	is.NoErr(err)
	is.Equal(string(got), `{"seq":2,"region":"u\"s\\","q":null}`)

	got, err = SimpleHeaderConverter{}.Marshal(&Schema{Type: TypeString}, "dbz")
	is.NoErr(err)
	is.Equal(string(got), "dbz") // top-level strings are not quoted

	got, err = SimpleHeaderConverter{}.Marshal(&Schema{Type: TypeString}, nil)
	is.NoErr(err)
	is.True(got == nil)
}

func TestJavaBigDecimalString(t *testing.T) {
	for _, tc := range []struct {
		unscaled int64
		scale    int32
		want     string
	}{
		{123, 2, "1.23"},
		{-5, 0, "-5"},
		{1, 6, "0.000001"},
		{1, 7, "1E-7"},
		{12, 8, "1.2E-7"},
		{1, -2, "1E+2"},
		{0, 2, "0.00"},
	} {
		got := javaBigDecimalString(Decimal{Unscaled: big.NewInt(tc.unscaled), Scale: tc.scale})
		if got != tc.want {
			t.Errorf("BigDecimal(%d, %d).toString() = %q, want %q", tc.unscaled, tc.scale, got, tc.want)
		}
	}
}
