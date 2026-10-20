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

package debezium

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/matryer/is"
)

func TestAppendJSONString(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	// The escaping vector from the goldens (dbz.golden.customers, the "name"
	// column): tab, newline, control chars, quote, backslash, slash, angle
	// brackets, U+2028, an astral char, and DEL.
	in := "tab\there\nnl\x01\x1f \"q\" \\ / <&> \u2028 \U0001F600 \x7f"
	want := "tab\\there\\nnl\\u0001\\u001F \\\"q\\\" \\\\ / <&> \\u2028 \\uD83D\\uDE00 \x7f"

	var b bytes.Buffer
	appendJSONString(&b, in)
	is.Equal(b.String(), want)
}

func TestJavaBigIntBytes(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		n    int64
		want []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7f}},
		{128, []byte{0x00, 0x80}},
		{-1, []byte{0xff}},
		{-128, []byte{0x80}},
		{-129, []byte{0xff, 0x7f}},
		{-256, []byte{0xff, 0x00}},
		{255, []byte{0x00, 0xff}},
	}
	for _, c := range cases {
		got := javaBigIntBytes(big.NewInt(c.n))
		is.Equal(got, c.want)
	}
}

func TestDecimalUnscaled(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		s     string
		scale int
		want  int64
	}{
		{"-0.0001", 4, -1},
		{"1.10", 2, 110},
		{"-1", 0, -1},
		{"0", 0, 0},
	}
	for _, c := range cases {
		got, err := decimalUnscaled(c.s, c.scale)
		is.NoErr(err)
		is.Equal(got.Int64(), c.want)
	}
}

func TestVariableScaleDecimal(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		s     string
		scale int
		val   int64
	}{
		{"1.10", 1, 11},
		{"1.000", 0, 1},
		{"100", 0, 100},
		{"0.00", 0, 0},
		{"-0.5", 1, -5},
	}
	for _, c := range cases {
		scale, val, err := variableScaleDecimal(c.s)
		is.NoErr(err)
		is.Equal(scale, c.scale)
		is.Equal(val.Int64(), c.val)
	}
}

func TestAppendFloat(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		f    float64
		bits int
		want string
	}{
		{0, 64, "0.0"},
		{1, 64, "1.0"},
		{1e-300, 64, "1.0E-300"},
		{3.14, 64, "3.14"},
		{-2.5, 64, "-2.5"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		appendFloat(&b, c.f, c.bits)
		is.Equal(b.String(), c.want)
	}
}
