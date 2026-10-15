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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
	"github.com/goccy/go-json"
)

// DescriptorVersion is the only descriptor format version this encoder reads.
const DescriptorVersion = 1

// Descriptor is the parsed debezium.descriptor metadata: the Connect schemas
// of a row's key and value, exactly as Debezium would build them. The source
// writes it; the encoder never infers a type.
//
// Wire form (version 1):
//
//	{"version":1,"key":<schema>|null,"value":<schema>}
//
// where <schema> is a Kafka Connect schema in JsonConverter form (the
// "schema" member of a schemas.enable=true message). The value schema is the
// row struct named "<prefix>.<schema>.<table>.Value", optional, fields in
// column order; the key schema is "<...>.Key", required, key columns in key
// order, or null for a table without a key.
type Descriptor struct {
	Key   *connect.Schema
	Value *connect.Schema
}

// ParseDescriptor parses and validates a descriptor.
func ParseDescriptor(b []byte) (*Descriptor, error) {
	var raw struct {
		Version *int            `json:"version"`
		Key     json.RawMessage `json:"key"`
		Value   json.RawMessage `json:"value"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, conduiterr.Wrap(connect.CodeDescriptorMissing, "invalid "+MetaDescriptor+": "+err.Error(), err)
	}
	if raw.Version == nil {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, MetaDescriptor+" has no version")
	}
	if *raw.Version != DescriptorVersion {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, fmt.Sprintf(
			"%s version %d is not supported by this encoder (supports %d); upgrade Conduit or pin the source's debezium.format.version",
			MetaDescriptor, *raw.Version, DescriptorVersion,
		))
	}
	if len(raw.Value) == 0 || bytes.Equal(raw.Value, []byte("null")) {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, MetaDescriptor+" has no value schema")
	}
	d := &Descriptor{}
	var err error
	if d.Value, err = connect.ParseSchemaJSON(raw.Value); err != nil {
		return nil, err
	}
	if d.Value.Type != connect.TypeStruct {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, MetaDescriptor+" value schema is not a struct")
	}
	if len(raw.Key) > 0 && !bytes.Equal(raw.Key, []byte("null")) {
		if d.Key, err = connect.ParseSchemaJSON(raw.Key); err != nil {
			return nil, err
		}
		if d.Key.Type != connect.TypeStruct {
			return nil, conduiterr.New(connect.CodeDescriptorMissing, MetaDescriptor+" key schema is not a struct")
		}
	}
	return d, nil
}

// MarshalDescriptor writes a descriptor in its wire form. Sources and tests
// use it so the bytes the encoder reads are produced by the same converter
// code that writes Debezium's schemas.
func MarshalDescriptor(d *Descriptor) ([]byte, error) {
	conv := connect.NewJSONConverter()
	var buf bytes.Buffer
	buf.WriteString(`{"version":1,"key":`)
	if d.Key == nil {
		buf.WriteString("null")
	} else {
		b, err := conv.MarshalSchema(d.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteString(`,"value":`)
	b, err := conv.MarshalSchema(d.Value)
	if err != nil {
		return nil, err
	}
	buf.Write(b)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
