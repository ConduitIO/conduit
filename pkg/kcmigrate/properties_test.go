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

package kcmigrate

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

type kvWant struct {
	key, val  string
	line, col int
}

func parseProps(t *testing.T, content string) []kvWant {
	t.Helper()
	kvs, err := ParseProperties(Input{Name: "c.properties", Content: []byte(content)})
	if err != nil {
		t.Fatalf("ParseProperties: %v", err)
	}
	out := make([]kvWant, 0, len(kvs))
	for _, kv := range kvs {
		if kv.Origin.File != "c.properties" || kv.Origin.Pointer != "" {
			t.Fatalf("unexpected origin %+v", kv.Origin)
		}
		out = append(out, kvWant{kv.Key, kv.Value.Reveal(), kv.Origin.Line, kv.Origin.Column})
	}
	return out
}

// The expected values follow java.util.Properties.load(InputStream).
func TestParseProperties_JavaSemantics(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []kvWant
	}{
		{"equals", "a=b", []kvWant{{"a", "b", 1, 1}}},
		{"colon", "a:b", []kvWant{{"a", "b", 1, 1}}},
		{"whitespace separator", "a b", []kvWant{{"a", "b", 1, 1}}},
		{"spaces around separator", "  a  =  b  ", []kvWant{{"a", "b  ", 1, 3}}},
		{"whitespace then separator", "a \t= b", []kvWant{{"a", "b", 1, 1}}},
		{"second separator is value", "a==b", []kvWant{{"a", "=b", 1, 1}}},
		{"colon after equals is value", "a=:b", []kvWant{{"a", ":b", 1, 1}}},
		{"key only", "a", []kvWant{{"a", "", 1, 1}}},
		{"empty value", "a=", []kvWant{{"a", "", 1, 1}}},
		{"empty key", "=b", []kvWant{{"", "b", 1, 1}}},
		{"comments and blanks", "# x=y\n! z=w\n\n   \n a=b", []kvWant{{"a", "b", 5, 2}}},
		{"comment needs line start", "a=#b", []kvWant{{"a", "#b", 1, 1}}},
		{"escaped separator in key", `a\=b\:c\ d=e`, []kvWant{{"a=b:c d", "e", 1, 1}}},
		{"escapes", `a=\t\n\r\f\\\q\"`, []kvWant{{"a", "\t\n\r\f\\q\"", 1, 1}}},
		{"unicode escape", `k\u00e9=\u0041\u00df`, []kvWant{{"ké", "Aß", 1, 1}}},
		{"surrogate pair", `a=\ud83d\ude00`, []kvWant{{"a", "😀", 1, 1}}},
		{"lone surrogate", `a=\ud83dx`, []kvWant{{"a", "\ufffdx", 1, 1}}},
		{"continuation", "a=one\\\n    two\\\n\tthree", []kvWant{{"a", "onetwothree", 1, 1}}},
		{"continuation in key", "ke\\\n  y=v", []kvWant{{"key", "v", 1, 1}}},
		{"continuation line starting with hash", "a=1\\\n#2", []kvWant{{"a", "1#2", 1, 1}}},
		{"even backslashes do not continue", "a=x\\\\\nb=y", []kvWant{{"a", `x\`, 1, 1}, {"b", "y", 2, 1}}},
		{"comment backslash does not continue", "# c\\\na=b", []kvWant{{"a", "b", 2, 1}}},
		{"continuation into blank line ends it", "a=1\\\n\nb=2", []kvWant{{"a", "1", 1, 1}, {"b", "2", 3, 1}}},
		{"trailing backslash at EOF", "a=1\\", []kvWant{{"a", "1", 1, 1}}},
		{"CRLF", "a=1\r\nb=2\r\n", []kvWant{{"a", "1", 1, 1}, {"b", "2", 2, 1}}},
		{"CR only", "a=1\rb=2", []kvWant{{"a", "1", 1, 1}, {"b", "2", 2, 1}}},
		{"CRLF continuation", "a=1\\\r\n  2", []kvWant{{"a", "12", 1, 1}}},
		{"form feed is whitespace", "\fa\f=\fb", []kvWant{{"a", "b", 1, 2}}},
		{"duplicates kept in order", "a=1\nb=2\na=3", []kvWant{{"a", "1", 1, 1}, {"b", "2", 2, 1}, {"a", "3", 3, 1}}},
		{"latin-1 bytes", "a=\xe9", []kvWant{{"a", "é", 1, 1}}},
		{"utf-8 read as latin-1, as Java does", "a=é", []kvWant{{"a", "Ã©", 1, 1}}},
		{"empty", "", []kvWant{}},
		// Java 9+ LineReader: a comment is recognized while the logical line is
		// still empty, including after a continuation that left it empty, and
		// an emptied logical line produces no key.
		{"comment after emptied continuation", "\\\n#hidden=1\nb=2", []kvWant{{"b", "2", 3, 1}}},
		{"bang comment after emptied continuation", "  \\\n  !hidden\nb=2", []kvWant{{"b", "2", 3, 1}}},
		{"emptied continuation into blank line", "\\\n\nb=2", []kvWant{{"b", "2", 3, 1}}},
		{"lone continuation at EOF", "\\", []kvWant{{"", "", 1, 1}}}, // Java returns the emptied line at EOF
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(parseProps(t, tt.content), tt.want)
		})
	}
}

func TestParseProperties_MalformedUnicodeEscape(t *testing.T) {
	for _, content := range []string{`a=\u12`, `a=\u12g4`, "x=1\n" + `\uZZZZ=1`} {
		t.Run(content, func(t *testing.T) {
			is := is.New(t)
			_, err := ParseProperties(Input{Name: "c.properties", Content: []byte(content)})
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeInputUnreadable)
			is.True(strings.HasPrefix(ce.ConfigPath, "c.properties:"))
			is.True(ce.Suggestion != "")
		})
	}
}

// TestParseProperties_RoundTrip is a property test: any list of keys and
// values, written with Java's escaping (Properties.store), parses back to the
// same list. Seeded, so a failure reproduces.
func TestParseProperties_RoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []rune("ab=:# !\\\t\n\r\f é😀\u0000ÿ")
	randStr := func() string {
		n := r.IntN(8)
		var sb strings.Builder
		for range n {
			sb.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		return sb.String()
	}
	for i := range 2000 {
		n := r.IntN(5)
		var want []kvWant
		var sb strings.Builder
		for j := range n {
			k, v := randStr(), randStr()
			want = append(want, kvWant{key: k, val: v, line: j + 1, col: 1})
			sb.WriteString(storeEscape(k, true) + "=" + storeEscape(v, false) + "\n")
		}
		kvs, err := ParseProperties(Input{Name: "p", Content: []byte(sb.String())})
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(kvs) != len(want) {
			t.Fatalf("case %d: got %d keys, want %d\ninput: %q", i, len(kvs), len(want), sb.String())
		}
		for j, kv := range kvs {
			if kv.Key != want[j].key || kv.Value.Reveal() != want[j].val || kv.Origin.Line != want[j].line {
				t.Fatalf("case %d key %d: got %q=%q line %d, want %q=%q line %d\ninput: %q",
					i, j, kv.Key, kv.Value.Reveal(), kv.Origin.Line, want[j].key, want[j].val, want[j].line, sb.String())
			}
		}
	}
}

// storeEscape mirrors java.util.Properties.saveConvert with escUnicode=true.
func storeEscape(s string, isKey bool) string {
	var sb strings.Builder
	for i, c := range s {
		switch {
		case c == ' ' && (i == 0 || isKey):
			sb.WriteString(`\ `)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\f':
			sb.WriteString(`\f`)
		case strings.ContainsRune(`=:#!\`, c):
			sb.WriteString(`\` + string(c))
		case c < 0x20 || c > 0x7e:
			for _, u := range utf16Units(c) {
				fmt.Fprintf(&sb, `\u%04X`, u)
			}
		default:
			sb.WriteRune(c)
		}
	}
	return sb.String()
}

func utf16Units(c rune) []rune {
	if c < 0x10000 {
		return []rune{c}
	}
	c -= 0x10000
	return []rune{0xd800 + (c >> 10), 0xdc00 + (c & 0x3ff)}
}
