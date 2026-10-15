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
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

func addParseSeeds(f *testing.F) {
	f.Helper()
	paths, err := filepath.Glob("testdata/parse/*")
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	for _, s := range []string{
		"a=b\nc:d\ne f\n", "k=v\\\n  w", "# c\n! c\n", `a=\u00`, "\r\n\r", "=",
		`{"name":"n","config":{"a":"b","a":1}}`, `{"a":"b"}`, `{"x":{"info":{"config":{}}}}`, `[`, `{"a":`,
	} {
		f.Add([]byte(s))
	}
}

// FuzzParseProperties: never panics; fails only with a coded error; every
// key has an origin inside the input; the same input parses the same way.
func FuzzParseProperties(f *testing.F) {
	addParseSeeds(f)
	f.Fuzz(func(t *testing.T, content []byte) {
		in := Input{Name: "f.properties", Content: content}
		kvs, err := ParseProperties(in)
		if err != nil {
			ce, ok := conduiterr.Get(err)
			if !ok || ce.Code != CodeInputUnreadable {
				t.Fatalf("uncoded or unexpected error: %v", err)
			}
			return
		}
		lines := bytes.Count(content, []byte("\n")) + bytes.Count(content, []byte("\r")) + 1
		for _, kv := range kvs {
			if kv.Origin.Line < 1 || kv.Origin.Line > lines || kv.Origin.Column < 1 || kv.Origin.File != in.Name {
				t.Fatalf("origin %+v outside the input", kv.Origin)
			}
		}
		again, err := ParseProperties(in)
		if err != nil || !reflect.DeepEqual(kvs, again) {
			t.Fatalf("parse is not deterministic")
		}
	})
}

// FuzzParseConnectors: never panics for any format; fails only with one of
// the input codes; on success every config key carries an origin in the
// named input, and no key was lost (the JSON shapes keep at least one KV per
// config member, which the unit tests pin exactly).
func FuzzParseConnectors(f *testing.F) {
	addParseSeeds(f)
	f.Fuzz(func(t *testing.T, content []byte) {
		for _, format := range []Format{"", FormatProperties, FormatJSON, FormatRESTExport} {
			in := Input{Name: "f", Content: content}
			cs, err := ParseConnectors(in, format)
			if err != nil {
				ce, ok := conduiterr.Get(err)
				if !ok {
					t.Fatalf("uncoded error: %v", err)
				}
				switch ce.Code {
				case CodeInputUnreadable, CodeInputUnrecognized, CodeInputTooLarge:
				default:
					t.Fatalf("unexpected code %s", ce.Code)
				}
				continue
			}
			for _, c := range cs {
				for _, kv := range c.Config {
					if kv.Origin.File != in.Name || (kv.Origin.Pointer == "") == (kv.Origin.Line == 0) {
						t.Fatalf("bad origin %+v", kv.Origin)
					}
				}
			}
		}
	})
}
