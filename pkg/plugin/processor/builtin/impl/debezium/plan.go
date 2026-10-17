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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
)

// frame is the message-plan payload the encoder writes into Payload.After
// with kafka.messages.version=1. The Kafka destination decodes it and
// produces every message before acknowledging the record (design: "One WAL
// change, several messages").
//
// Key, value and header values are base64 strings with an explicit null state:
//   - key: null = absent (Kafka null key); "" = empty bytes; else base64.
//   - value: null = tombstone (Kafka null value); else base64.
//   - header value: null = header with no value; else base64.
type frame struct {
	Version  int       `json:"version"`
	Messages []message `json:"messages"`
}

type message struct {
	Topic   string       `json:"topic"`
	Key     *string      `json:"key"`
	Value   *string      `json:"value"`
	Headers []planHeader `json:"headers,omitempty"`
}

type planHeader struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// header names of a primary-key change.
const (
	headerNewKey = "__debezium.newkey"
	headerOldKey = "__debezium.oldkey"
)

// expandPlan turns one record into the ordered list of messages Debezium
// would have produced for that WAL change, each with its serialized key,
// value and headers (design table "One WAL change, several messages").
func expandPlan(rec opencdc.Record, d *descriptor, cfg encodeConfig, now time.Time) ([]message, error) {
	op := rec.Metadata[metaOp]
	topic := rec.Metadata[metaTopic]
	if topic == "" {
		return nil, newCoded(CodeDescriptorMissing, "record has no debezium.topic metadata",
			"Run this processor only on records produced by the Postgres source in debezium mode.")
	}

	keySchema := d.Key
	src, err := sourcePayload(rec.Metadata)
	if err != nil {
		return nil, err
	}
	tx := transactionPayload(rec.Metadata)

	switch op {
	case "c", "r":
		key, err := serializeKey(keySchema, rec.Key, cfg)
		if err != nil {
			return nil, err
		}
		val, err := serializeValue(nil, rec.Payload.After, d, cfg, src, tx, op, now)
		if err != nil {
			return nil, err
		}
		return []message{{Topic: topic, Key: key, Value: val}}, nil

	case "u":
		if rec.Metadata[metaOldKey] == "" {
			key, err := serializeKey(keySchema, rec.Key, cfg)
			if err != nil {
				return nil, err
			}
			val, err := serializeValue(rec.Payload.Before, rec.Payload.After, d, cfg, src, tx, "u", now)
			if err != nil {
				return nil, err
			}
			return []message{{Topic: topic, Key: key, Value: val}}, nil
		}
		return pkChangePlan(rec, keySchema, d, cfg, src, tx, topic, now)

	case "d":
		key, err := serializeKey(keySchema, rec.Key, cfg)
		if err != nil {
			return nil, err
		}
		delVal, err := serializeValue(rec.Payload.Before, nil, d, cfg, src, tx, "d", now)
		if err != nil {
			return nil, err
		}
		msgs := []message{{Topic: topic, Key: key, Value: delVal}}
		if cfg.TombstonesOnDelete {
			msgs = append(msgs, message{Topic: topic, Key: key, Value: nil})
		}
		return msgs, nil

	default:
		return nil, newCoded(CodeUnsupportedType,
			fmt.Sprintf("debezium.op %q is not supported by this encoder", op),
			"This revision encodes create, read, update and delete events; truncate and logical-decoding messages arrive in a later revision.")
	}
}

// pkChangePlan builds the three messages of a primary-key change: a delete
// and a tombstone for the old key, then a create for the new key (design:
// RelationalChangeRecordEmitter.emitUpdateAsPrimaryKeyChangeRecord).
func pkChangePlan(rec opencdc.Record, keySchema *schema, d *descriptor, cfg encodeConfig, src, tx map[string]any, topic string, now time.Time) ([]message, error) {
	oldKeyText := rec.Metadata[metaOldKey]
	oldKey, err := parseOldKey(oldKeyText)
	if err != nil {
		return nil, newCoded(CodeOldKeyUnavailable,
			"primary-key change carries an unparsable debezium.old_key: "+err.Error(),
			"Run this processor only on records produced by the Postgres source in debezium mode.")
	}
	newKey, err := recordKey(rec.Key)
	if err != nil {
		return nil, err
	}

	oldKeyBytes, err := serializeKeyMap(keySchema, oldKey, cfg)
	if err != nil {
		return nil, err
	}
	newKeyBytes, err := serializeKeyMap(keySchema, newKey, cfg)
	if err != nil {
		return nil, err
	}
	newKeyHeader, err := keyHeaderValue(keySchema, newKey)
	if err != nil {
		return nil, err
	}
	oldKeyHeader, err := keyHeaderValue(keySchema, oldKey)
	if err != nil {
		return nil, err
	}

	delVal, err := serializeValue(rec.Payload.Before, nil, d, cfg, src, tx, "d", now)
	if err != nil {
		return nil, err
	}
	createVal, err := serializeValue(nil, rec.Payload.After, d, cfg, src, tx, "c", now)
	if err != nil {
		return nil, err
	}

	return []message{
		{Topic: topic, Key: oldKeyBytes, Value: delVal, Headers: []planHeader{{Key: headerNewKey, Value: &newKeyHeader}}},
		{Topic: topic, Key: oldKeyBytes, Value: nil, Headers: []planHeader{{Key: headerNewKey, Value: &newKeyHeader}}},
		{Topic: topic, Key: newKeyBytes, Value: createVal, Headers: []planHeader{{Key: headerOldKey, Value: &oldKeyHeader}}},
	}, nil
}

// recordKey extracts the record key as a map.
func recordKey(k opencdc.Data) (map[string]any, error) {
	sd, ok := k.(opencdc.StructuredData)
	if !ok {
		if k == nil {
			return nil, nil
		}
		return nil, newCoded(CodeRecordSchemaMismatch, "record key is not structured", "")
	}
	return map[string]any(sd), nil
}

// parseOldKey parses the schemaless key JSON in debezium.old_key, preserving
// number text with json.Number so decimal scale round-trips.
func parseOldKey(text string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// serializeKey serializes the record key with the key converter and returns
// its base64, or nil for an absent key.
func serializeKey(keySchema *schema, key opencdc.Data, cfg encodeConfig) (*string, error) {
	m, err := recordKey(key)
	if err != nil {
		return nil, err
	}
	return serializeKeyMap(keySchema, m, cfg)
}

// serializeKeyMap serializes a key map with the key schema and converter.
func serializeKeyMap(keySchema *schema, key map[string]any, cfg encodeConfig) (*string, error) {
	if keySchema == nil || key == nil {
		return nil, nil // absent
	}
	var b bytes.Buffer
	if cfg.KeySchemasEnable {
		b.WriteString(`{"schema":`)
		keySchema.write(&b, "")
		b.WriteString(`,"payload":`)
	}
	if err := writeValue(&b, keySchema, key, cfg.DecimalFormat); err != nil {
		return nil, err
	}
	if cfg.KeySchemasEnable {
		b.WriteByte('}')
	}
	return b64(b.String()), nil
}

// serializeValue writes the envelope value (before/after are per-message row
// values, nil = null) with the value converter, and returns its base64.
func serializeValue(before, after any, d *descriptor, cfg encodeConfig, src, tx map[string]any, op string, now time.Time) (*string, error) {
	env := envelopeSchema(d.Value, cfg.Compat)
	srcSchema := sourceSchema(cfg.Compat)

	var payload bytes.Buffer
	if err := writeEnvelopePayload(&payload, before, after, d.Value, srcSchema, src, tx, op, cfg.DecimalFormat, now.UnixMilli(), now.UnixMicro(), now.UnixNano()); err != nil {
		return nil, err
	}

	var b bytes.Buffer
	if cfg.ValueSchemasEnable {
		b.WriteString(`{"schema":`)
		env.write(&b, "")
		b.WriteString(`,"payload":`)
		b.Write(payload.Bytes())
		b.WriteByte('}')
	} else {
		b.Write(payload.Bytes())
	}
	return b64(b.String()), nil
}

// keyHeaderValue writes the schemaless key JSON used as the
// __debezium.newkey / __debezium.oldkey header value: fields in key order,
// decimals as JSON numbers, bytes as base64 (the SimpleHeaderConverter form).
func keyHeaderValue(keySchema *schema, key map[string]any) (string, error) {
	var b bytes.Buffer
	if err := writeValue(&b, keySchema, key, decimalNumeric); err != nil {
		return "", err
	}
	return *b64(b.String()), nil
}

// encodeFrame marshals the message plan.
func encodeFrame(msgs []message) ([]byte, error) {
	f := frame{Version: 1, Messages: msgs}
	return json.Marshal(f)
}

// b64 returns the base64 of s.
func b64(s string) *string {
	out := base64.StdEncoding.EncodeToString([]byte(s))
	return &out
}
