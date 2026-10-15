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
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
)

// Type is a Connect physical schema type, spelled as the JsonConverter spells
// it ("float" is Connect FLOAT32, "double" is FLOAT64).
type Type string

// The Connect physical types.
const (
	TypeInt8    Type = "int8"
	TypeInt16   Type = "int16"
	TypeInt32   Type = "int32"
	TypeInt64   Type = "int64"
	TypeFloat32 Type = "float"
	TypeFloat64 Type = "double"
	TypeBoolean Type = "boolean"
	TypeString  Type = "string"
	TypeBytes   Type = "bytes"
	TypeArray   Type = "array"
	TypeMap     Type = "map"
	TypeStruct  Type = "struct"
)

func (t Type) valid() bool {
	switch t {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64, TypeFloat32, TypeFloat64,
		TypeBoolean, TypeString, TypeBytes, TypeArray, TypeMap, TypeStruct:
		return true
	}
	return false
}

// Logical type names the encoder gives special value handling. Every other
// name (the io.debezium.time.* and io.debezium.data.* names that carry a plain
// physical value) is passed through as schema metadata only.
const (
	LogicalDecimal              = "org.apache.kafka.connect.data.Decimal"
	LogicalVariableScaleDecimal = "io.debezium.data.VariableScaleDecimal"
	ParamDecimalScale           = "scale"
)

// Param is one schema parameter. Parameters are a list, not a map, because the
// JsonConverter writes them in insertion order and that order is part of the
// output bytes.
type Param struct {
	Key   string
	Value string
}

// Field is one field of a struct schema.
type Field struct {
	Name   string
	Schema *Schema
}

// Schema is a Connect schema.
//
// A Schema is immutable once built and safe for concurrent use; the encoder
// shares one Schema between every record of the same shape.
type Schema struct {
	Type     Type
	Optional bool
	Name     string
	// Version is nil when the schema has no version; Connect distinguishes an
	// absent version from version 0 in the converter output.
	Version *int
	// Doc is nil when the schema has no doc.
	Doc        *string
	Parameters []Param
	// Default is the default value in the value representation of [FromGo]
	// output. HasDefault distinguishes a null default from no default.
	Default    any
	HasDefault bool

	Items  *Schema // array element schema
	Keys   *Schema // map key schema
	Values *Schema // map value schema
	Fields []Field // struct fields, in order
}

// Param returns the value of a schema parameter and whether it is set.
func (s *Schema) Param(key string) (string, bool) {
	for _, p := range s.Parameters {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// Field returns the struct field with the given name and its index, or -1.
func (s *Schema) Field(name string) (Field, int) {
	for i, f := range s.Fields {
		if f.Name == name {
			return f, i
		}
	}
	return Field{}, -1
}

// Validate checks that the schema is structurally complete: every type is
// known, arrays have items, maps have keys and values, struct field names are
// unique, and a Decimal has a numeric scale.
func (s *Schema) Validate() error {
	return s.validate("$")
}

//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func (s *Schema) validate(path string) error {
	if s == nil {
		return schemaErr(path, "schema is null")
	}
	if !s.Type.valid() {
		return schemaErr(path, fmt.Sprintf("unknown type %q", s.Type))
	}
	// Connect names are Java Strings: invalid UTF-8 cannot come from Kafka
	// Connect and could not be written back faithfully.
	if !utf8.ValidString(s.Name) || (s.Doc != nil && !utf8.ValidString(*s.Doc)) {
		return schemaErr(path, "name or doc is not valid UTF-8")
	}
	for _, p := range s.Parameters {
		if !utf8.ValidString(p.Key) || !utf8.ValidString(p.Value) {
			return schemaErr(path, "parameter is not valid UTF-8")
		}
	}
	switch s.Type { //nolint:exhaustive // the remaining types share the default branch
	case TypeArray:
		if s.Items == nil {
			return schemaErr(path, "array schema without items")
		}
		if err := s.Items.validate(path + ".items"); err != nil {
			return err
		}
	case TypeMap:
		if s.Keys == nil || s.Values == nil {
			return schemaErr(path, "map schema without keys or values")
		}
		if err := s.Keys.validate(path + ".keys"); err != nil {
			return err
		}
		if err := s.Values.validate(path + ".values"); err != nil {
			return err
		}
	case TypeStruct:
		seen := make(map[string]bool, len(s.Fields))
		for _, f := range s.Fields {
			if seen[f.Name] {
				return schemaErr(path, fmt.Sprintf("duplicate field %q", f.Name))
			}
			if !utf8.ValidString(f.Name) {
				return schemaErr(path, fmt.Sprintf("field name %q is not valid UTF-8", f.Name))
			}
			seen[f.Name] = true
			if err := f.Schema.validate(path + "." + f.Name); err != nil {
				return err
			}
		}
	default:
		if s.Items != nil || s.Keys != nil || s.Values != nil || s.Fields != nil {
			return schemaErr(path, fmt.Sprintf("%s schema with child schemas", s.Type))
		}
	}
	if s.Name == LogicalDecimal {
		if s.Type != TypeBytes {
			return schemaErr(path, "Decimal must have type bytes")
		}
		if _, err := s.decimalScale(); err != nil {
			return schemaErr(path, err.Error())
		}
	}
	return nil
}

func (s *Schema) decimalScale() (int32, error) {
	v, ok := s.Param(ParamDecimalScale)
	if !ok {
		return 0, cerrors.New("Decimal schema without a scale parameter")
	}
	var scale int32
	if _, err := fmt.Sscanf(v, "%d", &scale); err != nil || fmt.Sprint(scale) != v {
		return 0, cerrors.Errorf("Decimal scale %q is not an integer", v)
	}
	return scale, nil
}

func schemaErr(path, msg string) error {
	return conduiterr.New(CodeDescriptorMissing, fmt.Sprintf("invalid Connect schema at %s: %s", path, msg))
}

// ParseSchemaJSON parses a schema in the JsonConverter's schema form (the
// "schema" member of a schemas.enable=true message), preserving field and
// parameter order. Defaults are converted into the value representation of
// [FromGo] using the schema they belong to. The result is validated.
//
// This is the descriptor format: the Postgres source writes the row's value
// and key schemas in this form, so the encoder never has to guess a type.
func ParseSchemaJSON(b []byte) (*Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	s, err := parseSchema(dec, 0)
	if err != nil {
		return nil, conduiterr.Wrap(CodeDescriptorMissing, "invalid Connect schema: "+err.Error(), err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, conduiterr.New(CodeDescriptorMissing, "invalid Connect schema: trailing data")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// maxSchemaDepth bounds recursion on untrusted input (fuzzing, a corrupt
// descriptor). Real row schemas nest at most a few levels.
const maxSchemaDepth = 64

//nolint:gocyclo // one branch per Connect type; splitting it hides the mapping
func parseSchema(dec *json.Decoder, depth int) (*Schema, error) {
	if depth > maxSchemaDepth {
		return nil, cerrors.New("schema nested too deeply")
	}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	s := &Schema{}
	var rawDefault json.RawMessage
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, cerrors.Errorf("expected object key, got %v", tok)
		}
		if seen[key] {
			return nil, cerrors.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		switch key {
		case "type":
			v, err := stringToken(dec)
			if err != nil {
				return nil, err
			}
			s.Type = Type(v)
		case "optional":
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			b, ok := tok.(bool)
			if !ok {
				return nil, cerrors.New("optional must be a boolean")
			}
			s.Optional = b
		case "name":
			if s.Name, err = stringToken(dec); err != nil {
				return nil, err
			}
		case "version":
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			n, ok := tok.(json.Number)
			if !ok {
				return nil, cerrors.New("version must be a number")
			}
			v, err := n.Int64()
			if err != nil || v < -1<<31 || v > 1<<31-1 {
				return nil, cerrors.New("version must be a 32-bit integer")
			}
			iv := int(v)
			s.Version = &iv
		case "doc":
			d, err := stringToken(dec)
			if err != nil {
				return nil, err
			}
			s.Doc = &d
		case "parameters":
			if err := expectDelim(dec, '{'); err != nil {
				return nil, err
			}
			s.Parameters = []Param{}
			pseen := map[string]bool{}
			for dec.More() {
				k, err := stringToken(dec)
				if err != nil {
					return nil, err
				}
				if pseen[k] {
					return nil, cerrors.Errorf("duplicate parameter %q", k)
				}
				pseen[k] = true
				v, err := stringToken(dec)
				if err != nil {
					return nil, err
				}
				s.Parameters = append(s.Parameters, Param{Key: k, Value: v})
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, err
			}
		case "default":
			if err := dec.Decode(&rawDefault); err != nil {
				return nil, err
			}
		case "items":
			if s.Items, err = parseSchema(dec, depth+1); err != nil {
				return nil, err
			}
		case "keys":
			if s.Keys, err = parseSchema(dec, depth+1); err != nil {
				return nil, err
			}
		case "values":
			if s.Values, err = parseSchema(dec, depth+1); err != nil {
				return nil, err
			}
		case "fields":
			if err := expectDelim(dec, '['); err != nil {
				return nil, err
			}
			s.Fields = []Field{}
			for dec.More() {
				fs, name, err := parseField(dec, depth+1)
				if err != nil {
					return nil, err
				}
				s.Fields = append(s.Fields, Field{Name: name, Schema: fs})
			}
			if err := expectDelim(dec, ']'); err != nil {
				return nil, err
			}
		case "field":
			// Only valid inside a struct's field list; parseField handles it.
			return nil, cerrors.New(`"field" outside a struct field`)
		default:
			return nil, cerrors.Errorf("unknown schema key %q", key)
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if s.Type == "" {
		return nil, cerrors.New("schema without type")
	}
	if rawDefault != nil {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		def, err := defaultFromJSON(s, rawDefault)
		if err != nil {
			return nil, cerrors.Errorf("default: %w", err)
		}
		s.Default, s.HasDefault = def, true
	}
	return s, nil
}

// parseField parses one entry of a struct's "fields" array: a schema object
// that also carries a "field" key with the field name.
func parseField(dec *json.Decoder, depth int) (*Schema, string, error) {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, "", err
	}
	// Split off "field" and parse the rest as a schema. Re-encoding through a
	// token stream keeps the parameter order of the remaining keys.
	inner := json.NewDecoder(bytes.NewReader(raw))
	inner.UseNumber()
	if err := expectDelim(inner, '{'); err != nil {
		return nil, "", err
	}
	var name *string
	var rest bytes.Buffer
	rest.WriteByte('{')
	first := true
	for inner.More() {
		k, err := stringToken(inner)
		if err != nil {
			return nil, "", err
		}
		var v json.RawMessage
		if err := inner.Decode(&v); err != nil {
			return nil, "", err
		}
		if k == "field" {
			if name != nil {
				return nil, "", cerrors.New(`duplicate "field" key`)
			}
			var n string
			if err := json.Unmarshal(v, &n); err != nil {
				return nil, "", cerrors.New("field name must be a string")
			}
			name = &n
			continue
		}
		if !first {
			rest.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		rest.Write(kb)
		rest.WriteByte(':')
		rest.Write(v)
	}
	if err := expectDelim(inner, '}'); err != nil {
		return nil, "", err
	}
	rest.WriteByte('}')
	if name == nil {
		return nil, "", cerrors.New(`struct field without "field" name`)
	}
	sd := json.NewDecoder(&rest)
	sd.UseNumber()
	s, err := parseSchema(sd, depth)
	if err != nil {
		return nil, "", cerrors.Errorf("field %q: %w", *name, err)
	}
	return s, *name, nil
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if got, ok := tok.(json.Delim); !ok || got != d {
		return cerrors.Errorf("expected %q, got %v", d, tok)
	}
	return nil
}

func stringToken(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", cerrors.Errorf("expected string, got %v", tok)
	}
	return s, nil
}
