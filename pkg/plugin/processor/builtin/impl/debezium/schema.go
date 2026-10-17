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
	"encoding/json"
	"fmt"
	"strconv"
)

// Connect schema type names, as the JsonConverter writes them.
const (
	typeInt16   = "int16"
	typeInt32   = "int32"
	typeInt64   = "int64"
	typeFloat   = "float"
	typeDouble  = "double"
	typeBoolean = "boolean"
	typeString  = "string"
	typeBytes   = "bytes"
	typeArray   = "array"
	typeMap     = "map"
	typeStruct  = "struct"
)

// Logical type names Debezium uses. The value writer switches on these.
const (
	nameDecimal              = "org.apache.kafka.connect.data.Decimal"
	nameVariableScaleDecimal = "io.debezium.data.VariableScaleDecimal"
	nameEnum                 = "io.debezium.data.Enum"
)

// schema is a Kafka Connect schema in the JsonConverter's ordered form. Field
// order, parameter order and default presence are part of the schema text
// (the Schema Registry dedupes on it), so they are preserved exactly.
type schema struct {
	Type     string
	Items    *schema // array element schema
	Fields   []field // struct fields, in column order
	Optional bool
	Name     string
	Version  int // 0: none
	Doc      string
	Params   []parameter // ordered
	// Default is the schema default as a raw JSON literal (JsonConverter's
	// NUMERIC decimal form). Nil: no default.
	Default json.RawMessage
	// Field is the field name when this schema is a struct field.
	Field string
}

// field is one struct field.
type field struct {
	Name   string
	Schema *schema
}

// parameter is one schema parameter, in Debezium's order.
type parameter struct{ Key, Value string }

// descriptor is the debezium.descriptor metadata value:
//
//	{"version":1,"key":<schema|null>,"value":<schema>}
type descriptor struct {
	Version int
	Key     *schema // nil: table without a key
	Value   *schema
}

// parseDescriptor parses a debezium.descriptor metadata value. It returns
// CodeDescriptorMissing for an absent, empty, malformed or newer descriptor.
func parseDescriptor(text string) (*descriptor, error) {
	if text == "" {
		return nil, newCoded(CodeDescriptorMissing, "record has no debezium.descriptor",
			"Run this processor only on records produced by the Postgres source in debezium mode.")
	}
	var d descriptor
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, newCoded(CodeDescriptorMissing, "debezium.descriptor is not a JSON object",
			"Run this processor only on records produced by the Postgres source in debezium mode.")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, newCoded(CodeDescriptorMissing, "malformed debezium.descriptor", "Regenerate the descriptor from the Postgres source.")
		}
		key := keyTok.(string)
		switch key {
		case "version":
			var v int
			if err := dec.Decode(&v); err != nil {
				return nil, newCoded(CodeDescriptorMissing, "malformed debezium.descriptor version", "Regenerate the descriptor from the Postgres source.")
			}
			d.Version = v
		case "key":
			if err := decodeDescriptorSchema(dec, &d.Key); err != nil {
				return nil, newCoded(CodeDescriptorMissing, "malformed debezium.descriptor key schema: "+err.Error(), "Regenerate the descriptor from the Postgres source.")
			}
		case "value":
			if err := decodeDescriptorSchema(dec, &d.Value); err != nil {
				return nil, newCoded(CodeDescriptorMissing, "malformed debezium.descriptor value schema: "+err.Error(), "Regenerate the descriptor from the Postgres source.")
			}
		default:
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, newCoded(CodeDescriptorMissing, "malformed debezium.descriptor", "Regenerate the descriptor from the Postgres source.")
	}
	if d.Version != descriptorVersion {
		return nil, newCoded(CodeDescriptorMissing,
			fmt.Sprintf("debezium.descriptor has version %d, this encoder reads version %d", d.Version, descriptorVersion),
			"Upgrade the encoder or the Postgres connector so their descriptor versions match.")
	}
	if d.Value == nil {
		return nil, newCoded(CodeDescriptorMissing, "debezium.descriptor has no value schema", "Regenerate the descriptor from the Postgres source.")
	}
	return &d, nil
}

const descriptorVersion = 1

// decodeDescriptorSchema decodes one schema value ("key" or "value"), which
// is either null or a Connect schema object.
func decodeDescriptorSchema(dec *json.Decoder, dst **schema) error {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*dst = nil
		return nil
	}
	s, err := parseSchema(raw)
	if err != nil {
		return err
	}
	*dst = s
	return nil
}

// parseSchema parses a Connect schema object in the JsonConverter form,
// preserving field order, parameter order and the raw default.
func parseSchema(raw json.RawMessage) (*schema, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return parseSchemaObject(dec)
}

func parseSchemaObject(dec *json.Decoder) (*schema, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("schema must be a JSON object")
	}
	s := &schema{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		switch key {
		case "type":
			if s.Type, err = decodeString(dec); err != nil {
				return nil, err
			}
		case "items":
			if s.Items, err = parseSchemaObject(dec); err != nil {
				return nil, err
			}
		case "fields":
			if s.Fields, err = parseFields(dec); err != nil {
				return nil, err
			}
		case "optional":
			if s.Optional, err = decodeBool(dec); err != nil {
				return nil, err
			}
		case "name":
			if s.Name, err = decodeString(dec); err != nil {
				return nil, err
			}
		case "version":
			if s.Version, err = decodeInt(dec); err != nil {
				return nil, err
			}
		case "doc":
			if s.Doc, err = decodeString(dec); err != nil {
				return nil, err
			}
		case "parameters":
			if s.Params, err = parseParameters(dec); err != nil {
				return nil, err
			}
		case "default":
			if err := dec.Decode(&s.Default); err != nil {
				return nil, err
			}
		case "field":
			if s.Field, err = decodeString(dec); err != nil {
				return nil, err
			}
		default:
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return s, nil
}

func parseFields(dec *json.Decoder) ([]field, error) {
	if _, err := dec.Token(); err != nil { // consume '['
		return nil, err
	}
	var out []field
	for dec.More() {
		fs, err := parseSchemaObject(dec)
		if err != nil {
			return nil, err
		}
		out = append(out, field{Name: fs.Field, Schema: fs})
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, err
	}
	return out, nil
}

func parseParameters(dec *json.Decoder) ([]parameter, error) {
	if _, err := dec.Token(); err != nil { // consume '{'
		return nil, err
	}
	var out []parameter
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		val, err := decodeString(dec)
		if err != nil {
			return nil, err
		}
		out = append(out, parameter{Key: key, Value: val})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return out, nil
}

func decodeString(dec *json.Decoder) (string, error) {
	var s string
	err := dec.Decode(&s)
	return s, err
}

func decodeBool(dec *json.Decoder) (bool, error) {
	var b bool
	err := dec.Decode(&b)
	return b, err
}

func decodeInt(dec *json.Decoder) (int, error) {
	var n int
	err := dec.Decode(&n)
	return n, err
}

// write writes the schema in the JsonConverter form, in its key order: type,
// items or fields, optional, name, version, doc, parameters, default, field.
func (s *schema) write(b *bytes.Buffer, fieldName string) {
	b.WriteString(`{"type":`)
	writeJSONString(b, s.Type)
	if s.Items != nil {
		b.WriteString(`,"items":`)
		s.Items.write(b, "")
	}
	if s.Type == typeStruct {
		b.WriteString(`,"fields":[`)
		for i, f := range s.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			f.Schema.write(b, f.Name)
		}
		b.WriteByte(']')
	}
	b.WriteString(`,"optional":`)
	b.WriteString(strconv.FormatBool(s.Optional))
	if s.Name != "" {
		b.WriteString(`,"name":`)
		writeJSONString(b, s.Name)
	}
	if s.Version != 0 {
		b.WriteString(`,"version":`)
		b.WriteString(strconv.Itoa(s.Version))
	}
	if s.Doc != "" {
		b.WriteString(`,"doc":`)
		writeJSONString(b, s.Doc)
	}
	if len(s.Params) > 0 {
		b.WriteString(`,"parameters":{`)
		for i, p := range s.Params {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, p.Key)
			b.WriteByte(':')
			writeJSONString(b, p.Value)
		}
		b.WriteByte('}')
	}
	if s.Default != nil {
		b.WriteString(`,"default":`)
		b.Write(s.Default)
	}
	if fieldName != "" {
		b.WriteString(`,"field":`)
		writeJSONString(b, fieldName)
	}
	b.WriteByte('}')
}

// MarshalJSON writes the schema in JsonConverter form.
func (s *schema) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	s.write(&b, "")
	return b.Bytes(), nil
}

// clone returns a shallow copy whose Optional and Field can be changed without
// touching the original.
func (s *schema) clone() *schema {
	c := *s
	return &c
}

// param returns the value of a parameter, or "" when absent.
func (s *schema) param(key string) string {
	for _, p := range s.Params {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

// writeJSONString writes s as a JSON string with Java/Jackson escaping (see
// appendJSONString).
func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	appendJSONString(b, s)
	b.WriteByte('"')
}
