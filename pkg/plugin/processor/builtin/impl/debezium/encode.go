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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
)

// Converters are the key, value and header converters of a pipeline, as in
// Kafka Connect's key.converter, value.converter and header.converter.
type Converters struct {
	Key    connect.JSONConverter
	Value  connect.JSONConverter
	Header connect.SimpleHeaderConverter
}

// EncodedMessage is one Kafka message ready to produce. A nil Key is a null
// key and a nil Value a null value (tombstone); both differ from empty.
type EncodedMessage struct {
	Topic   string
	Key     []byte
	Value   []byte
	Headers []EncodedHeader
}

// EncodedHeader is one serialized header.
type EncodedHeader struct {
	Key   string
	Value []byte
}

// Encode serializes a message with the given converters.
func (m Message) Encode(c Converters) (EncodedMessage, error) {
	out := EncodedMessage{Topic: m.Topic}
	// Invariant 6: a value without a schema would otherwise be written as a
	// null (a key-less message or a tombstone), silently discarding it.
	if (m.KeySchema == nil && m.Key != nil) || (m.ValueSchema == nil && m.Value != nil) {
		return EncodedMessage{}, conduiterr.New(connect.CodeRecordSchemaMismatch, "message has a key or value without a schema")
	}
	var err error
	if m.KeySchema != nil {
		if out.Key, err = c.Key.Marshal(m.KeySchema, m.Key); err != nil {
			return EncodedMessage{}, err
		}
	}
	if m.ValueSchema != nil {
		if out.Value, err = c.Value.Marshal(m.ValueSchema, m.Value); err != nil {
			return EncodedMessage{}, err
		}
	}
	for _, h := range m.Headers {
		v, err := c.Header.Marshal(h.Schema, h.Value)
		if err != nil {
			return EncodedMessage{}, err
		}
		out.Headers = append(out.Headers, EncodedHeader{Key: h.Key, Value: v})
	}
	return out, nil
}
