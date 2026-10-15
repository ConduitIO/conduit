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

package harness

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func keyList(ks []KeyOccurrence) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Key
	}
	return out
}

func TestParseProperties(t *testing.T) {
	in := strings.Join([]string{
		"# comment = not a key",
		"   ! also a comment \\",
		"ignored.after.comment.continuation=1", // the comment above does not continue
		"",
		"a=1",
		"b : 2",
		"c 3",
		"d\t=\t4",
		"multi=first,\\",
		"    second,\\",
		"    third",
		`esc\=aped\:key=v`,
		`unicode=café`,
		"empty=",
		"novalue",
		"a=duplicate",
		`even.backslashes=ends\\`,
		"next=line",
	}, "\r\n")
	got := parseProperties([]byte(in))

	want := []KeyOccurrence{
		{Key: "ignored.after.comment.continuation", Value: "1", Line: 3},
		{Key: "a", Value: "1", Line: 5},
		{Key: "b", Value: "2", Line: 6},
		{Key: "c", Value: "3", Line: 7},
		{Key: "d", Value: "4", Line: 8},
		{Key: "multi", Value: "first,second,third", Line: 9},
		{Key: "esc=aped:key", Value: "v", Line: 12},
		{Key: "unicode", Value: "café", Line: 13},
		{Key: "empty", Value: "", Line: 14},
		{Key: "novalue", Value: "", Line: 15},
		{Key: "a", Value: "duplicate", Line: 16},
		{Key: "even.backslashes", Value: `ends\`, Line: 17},
		{Key: "next", Value: "line", Line: 18},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("parseProperties mismatch (-want +got):\n%s", diff)
	}
}

func TestParseProperties_ContinuationAtEOF(t *testing.T) {
	is := is.New(t)
	got := parseProperties([]byte("a=b\\"))
	is.Equal(keyList(got), []string{"a"})
	is.Equal(got[0].Value, "b")
}

func TestParseInput_JSONShapes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		shape    string
		keys     []string
		pointers []string
	}{{
		name:     "wrapper counts config members only and keeps duplicates in order",
		in:       `{"name":"n","type":"source","config":{"connector.class":"X","a":1,"a":true,"b":null,"o":{"x":1}}}`,
		shape:    ShapeWrapper,
		keys:     []string{"connector.class", "a", "a", "b", "o"},
		pointers: []string{"/config/connector.class", "/config/a", "/config/a", "/config/b", "/config/o"},
	}, {
		name:     "bare map with escaped pointer",
		in:       `{"connector.class":"X","a/b~c":"v"}`,
		shape:    ShapeBareMap,
		keys:     []string{"connector.class", "a/b~c"},
		pointers: []string{"/connector.class", "/a~1b~0c"},
	}, {
		name:     "rest export",
		in:       `{"c1":{"info":{"name":"c1","config":{"connector.class":"X","k":"v"}},"status":{}},"c2":{"info":{"config":{"connector.class":"Y"}}}}`,
		shape:    ShapeRESTExport,
		keys:     []string{"connector.class", "k", "connector.class"},
		pointers: []string{"/c1/info/config/connector.class", "/c1/info/config/k", "/c2/info/config/connector.class"},
	}, {
		name:  "no connector",
		in:    `{"clusters":"A, B"}`,
		shape: ShapeUnrecognized,
	}, {
		name:  "array",
		in:    `[{"connector.class":"X"}]`,
		shape: ShapeUnrecognized,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			f, err := ParseInput("x.json", []byte(tt.in))
			is.NoErr(err)
			is.Equal(f.Shape, tt.shape)
			is.Equal(strings.Join(keyList(f.Keys), " "), strings.Join(tt.keys, " "))
			var ptrs []string
			for _, k := range f.Keys {
				ptrs = append(ptrs, k.Pointer)
			}
			is.Equal(strings.Join(ptrs, " "), strings.Join(tt.pointers, " "))
		})
	}
}

func TestParseInput_JSONValues(t *testing.T) {
	is := is.New(t)
	f, err := ParseInput("x.json", []byte(`{"connector.class":"X","n":1800000,"f":1.5,"b":false,"z":null,"s":"a\"b"}`))
	is.NoErr(err)
	got := map[string]string{}
	for _, k := range f.Keys {
		got[k.Key] = k.Value
	}
	is.Equal(got["n"], "1800000")
	is.Equal(got["f"], "1.5")
	is.Equal(got["b"], "false")
	is.Equal(got["z"], "null")
	is.Equal(got["s"], `a"b`)
}

func TestParseInput_Errors(t *testing.T) {
	for name, tc := range map[string]struct{ file, in string }{
		"invalid json":   {"x.json", `{"a":`},
		"trailing data":  {"x.json", `{"connector.class":"X"} {}`},
		"invalid utf8":   {"x.properties", "a=\xff"},
		"unknown suffix": {"x.yaml", "a: b"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInput(tc.file, []byte(tc.in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestCommonParentLabels(t *testing.T) {
	is := is.New(t)
	is.Equal(commonParentLabels([]string{"input/connector.json"}), []string{"connector.json"})
	is.Equal(commonParentLabels([]string{"input/worker.properties", "input/connector.json"}), []string{"worker.properties", "connector.json"})
	is.Equal(commonParentLabels([]string{"input/a/x.json", "input/b/y.json"}), []string{"a/x.json", "b/y.json"})
	is.Equal(commonParentLabels([]string{"input/a/x.json", "other/y.json"}), []string{"input/a/x.json", "other/y.json"})
}

// FuzzParseInput: the parsers never panic, and every properties key they
// return starts on a line that exists and is not a comment.
func FuzzParseInput(f *testing.F) {
	f.Add("x.properties", "a=1\n# c\nb:2\\\n 3\n")
	f.Add("x.properties", "\\\n\\\\\n=\n:x")
	f.Add("x.json", `{"name":"n","config":{"a":"b","a":"c"}}`)
	f.Add("x.json", `{"c":{"info":{"config":{"k":1}}}}`)
	f.Fuzz(func(t *testing.T, name, in string) {
		if !strings.HasSuffix(name, ".json") {
			name = "x.properties"
		}
		got, err := ParseInput(name, []byte(in))
		if err != nil || got.Format != FormatProperties {
			return
		}
		lines := splitNaturalLines(in)
		for _, k := range got.Keys {
			if k.Line < 1 || k.Line > len(lines) {
				t.Fatalf("key %q on line %d of %d", k.Key, k.Line, len(lines))
			}
			first := strings.TrimLeft(lines[k.Line-1], " \t\f")
			if first == "" || first[0] == '#' || first[0] == '!' {
				t.Fatalf("key %q starts on a blank or comment line %d", k.Key, k.Line)
			}
		}
	})
}
