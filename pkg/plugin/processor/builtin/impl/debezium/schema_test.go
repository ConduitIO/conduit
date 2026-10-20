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
	"testing"

	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestParseSchemaFull(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	raw := json.RawMessage(`{"type":"struct","fields":[{"type":"int32","optional":false,"field":"id"}],"optional":true,"name":"Foo","version":3,"doc":"doc","parameters":{"allowed":"true,false","scale":"2"},"default":"1.5","field":"f","extra":"ignored"}`)
	s, err := parseSchema(raw)
	is.NoErr(err)
	is.Equal(s.Type, typeStruct)
	is.Equal(len(s.Fields), 1)
	is.Equal(s.Fields[0].Name, "id")
	is.Equal(s.Fields[0].Schema.Type, typeInt32)
	is.Equal(s.Optional, true)
	is.Equal(s.Name, "Foo")
	is.Equal(s.Version, 3)
	is.Equal(s.Doc, "doc")
	is.Equal(len(s.Params), 2)
	is.Equal(s.param("allowed"), "true,false")
	is.Equal(s.param("scale"), "2")
	is.Equal(s.param("missing"), "")
	is.Equal(string(s.Default), `"1.5"`)
	is.Equal(s.Field, "f")
}

func TestParseSchemaItems(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	raw := json.RawMessage(`{"type":"array","items":{"type":"string","optional":true},"optional":false,"name":"Names"}`)
	s, err := parseSchema(raw)
	is.NoErr(err)
	is.Equal(s.Type, typeArray)
	is.Equal(s.Items.Type, typeString)
	is.Equal(s.Items.Optional, true)
	is.Equal(s.Name, "Names")
}

func TestSchemaMarshalJSON(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	s := &schema{Type: typeInt32, Optional: false, Name: "Foo", Version: 1}
	b, err := s.MarshalJSON()
	is.NoErr(err)
	is.Equal(string(b), `{"type":"int32","optional":false,"name":"Foo","version":1}`)
}

func TestParseDescriptorNullKey(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	text := `{"version":1,"key":null,"value":{"type":"struct","fields":[],"optional":false,"name":"V"}}`
	d, err := parseDescriptor(text)
	is.NoErr(err)
	is.Equal(d.Key, nil)
	is.True(d.Value != nil)
	is.Equal(d.Value.Name, "V")
}

func TestParseDescriptorErrors(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"not_object", `"foo"`},
		{"version_mismatch", `{"version":2,"key":null,"value":{"type":"struct","fields":[],"optional":false,"name":"V"}}`},
		{"no_value_schema", `{"version":1,"key":null}`},
		{"malformed", `{"version":1,`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseDescriptor(c.text)
			is.True(err != nil)
		})
	}
}
