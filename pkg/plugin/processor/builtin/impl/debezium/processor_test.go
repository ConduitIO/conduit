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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// compositeDescriptor returns the dbz.golden.composite descriptor: a
// two-column key (seq, region) and a three-column value (region, seq, note).
func compositeDescriptor() string {
	key := &schema{
		Type: typeStruct,
		Name: "dbz.golden.composite.Key",
		Fields: []field{
			{Name: "seq", Schema: &schema{Type: typeInt32, Optional: false}},
			{Name: "region", Schema: &schema{Type: typeString, Optional: false}},
		},
	}
	value := &schema{
		Type:     typeStruct,
		Optional: true,
		Name:     "dbz.golden.composite.Value",
		Fields: []field{
			{Name: "region", Schema: &schema{Type: typeString, Optional: false}},
			{Name: "seq", Schema: &schema{Type: typeInt32, Optional: false}},
			{Name: "note", Schema: &schema{Type: typeString, Optional: true}},
		},
	}
	var b bytes.Buffer
	b.WriteString(`{"version":1,"key":`)
	key.write(&b, "")
	b.WriteString(`,"value":`)
	value.write(&b, "")
	b.WriteByte('}')
	return b.String()
}

func compositeMetadata(op, oldKey string) opencdc.Metadata {
	md := opencdc.Metadata{
		metaCollection:               "golden.composite",
		metaTopic:                    "dbz.golden.composite",
		metaOp:                       op,
		metaDescriptor:               compositeDescriptor(),
		metaSourcePfx + srcVersion:   "3.7.0.Final",
		metaSourcePfx + srcConnector: "postgresql",
		metaSourcePfx + srcName:      "dbz",
		metaSourcePfx + srcTSMs:      "1792045811692",
		metaSourcePfx + srcSnapshot:  "false",
		metaSourcePfx + srcDB:        "golden",
		metaSourcePfx + srcSequence:  `["25308336","25308336"]`,
		metaSourcePfx + srcTSUs:      "1792045811692315",
		metaSourcePfx + srcTSNs:      "1792045811692315000",
		metaSourcePfx + srcSchema:    "golden",
		metaSourcePfx + srcTable:     "composite",
		metaSourcePfx + srcTxID:      "777",
		metaSourcePfx + srcLSN:       "25308336",
	}
	if oldKey != "" {
		md[metaOldKey] = oldKey
	}
	return md
}

func newEncodeConfig(converter, decimal string, schemas, tombstones bool) encodeConfig {
	return encodeConfig{
		KeyConverter:       converter,
		ValueConverter:     converter,
		KeySchemasEnable:   schemas,
		ValueSchemasEnable: schemas,
		DecimalFormat:      decimal,
		TombstonesOnDelete: tombstones,
		Compat:             Compat35,
	}
}

// encodeRecord runs the plan expansion and frame encoding, returning the
// decoded messages.
func encodeRecord(t *testing.T, rec opencdc.Record, cfg encodeConfig) []message {
	t.Helper()
	d, err := parseDescriptor(rec.Metadata[metaDescriptor])
	if err != nil {
		t.Fatalf("parseDescriptor: %v", err)
	}
	msgs, err := expandPlan(rec, d, cfg, time.UnixMilli(1792045811910))
	if err != nil {
		t.Fatalf("expandPlan: %v", err)
	}
	return msgs
}

func decodeStr(t *testing.T, s *string) string {
	t.Helper()
	if s == nil {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(*s)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return string(b)
}

func TestInsertExpandsToOneMessage(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	rec := opencdc.Record{
		Key:       opencdc.StructuredData{"seq": int32(1), "region": "us"},
		Payload:   opencdc.Change{After: opencdc.StructuredData{"region": "us", "seq": int32(1), "note": "second"}},
		Metadata:  compositeMetadata("c", ""),
		Operation: opencdc.OperationCreate,
	}
	msgs := encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, true, true))
	is.Equal(len(msgs), 1)
	is.Equal(msgs[0].Topic, "dbz.golden.composite")

	key := decodeStr(t, msgs[0].Key)
	is.Equal(key, `{"schema":{"type":"struct","fields":[{"type":"int32","optional":false,"field":"seq"},{"type":"string","optional":false,"field":"region"}],"optional":false,"name":"dbz.golden.composite.Key"},"payload":{"seq":1,"region":"us"}}`)

	var val map[string]any
	err := json.Unmarshal([]byte(decodeStr(t, msgs[0].Value)), &val)
	is.NoErr(err)
	payload := val["payload"].(map[string]any)
	is.Equal(payload["op"], "c")
	is.Equal(payload["before"], nil)
	after := payload["after"].(map[string]any)
	is.Equal(after["region"], "us")
	is.Equal(after["seq"], float64(1))
	is.Equal(after["note"], "second")

	schemaObj := val["schema"].(map[string]any)
	is.Equal(schemaObj["name"], "dbz.golden.composite.Envelope")
}

func TestDeleteExpandsToDeleteAndTombstone(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	rec := opencdc.Record{
		Key:       opencdc.StructuredData{"seq": int32(1), "region": "us"},
		Payload:   opencdc.Change{Before: opencdc.StructuredData{"region": "us", "seq": int32(1), "note": nil}},
		Metadata:  compositeMetadata("d", ""),
		Operation: opencdc.OperationDelete,
	}
	msgs := encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, true, true))
	is.Equal(len(msgs), 2)

	// delete event
	var del map[string]any
	err := json.Unmarshal([]byte(decodeStr(t, msgs[0].Value)), &del)
	is.NoErr(err)
	is.Equal(del["payload"].(map[string]any)["op"], "d")

	// tombstone: same key, null value
	is.Equal(msgs[1].Value, nil)
	is.Equal(*msgs[1].Key, *msgs[0].Key)

	// tombstones.on.delete=false collapses to one message
	msgs = encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, true, false))
	is.Equal(len(msgs), 1)
}

func TestPrimaryKeyChangeExpandsToThreeMessages(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	rec := opencdc.Record{
		Key: opencdc.StructuredData{"seq": int32(2), "region": "us"},
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{"region": "us", "seq": int32(1), "note": nil},
			After:  opencdc.StructuredData{"region": "us", "seq": int32(2), "note": "second"},
		},
		Metadata:  compositeMetadata("u", `{"seq":1,"region":"us"}`),
		Operation: opencdc.OperationUpdate,
	}
	msgs := encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, true, true))
	is.Equal(len(msgs), 3)

	keySchema := `{"type":"struct","fields":[{"type":"int32","optional":false,"field":"seq"},{"type":"string","optional":false,"field":"region"}],"optional":false,"name":"dbz.golden.composite.Key"}`
	oldKey := `{"schema":` + keySchema + `,"payload":{"seq":1,"region":"us"}}`
	newKey := `{"schema":` + keySchema + `,"payload":{"seq":2,"region":"us"}}`
	newKeyHeader := `{"seq":2,"region":"us"}`
	oldKeyHeader := `{"seq":1,"region":"us"}`

	// delete: old key, header __debezium.newkey = new key
	is.Equal(decodeStr(t, msgs[0].Key), oldKey)
	var del map[string]any
	err := json.Unmarshal([]byte(decodeStr(t, msgs[0].Value)), &del)
	is.NoErr(err)
	is.Equal(del["payload"].(map[string]any)["op"], "d")
	is.Equal(len(msgs[0].Headers), 1)
	is.Equal(msgs[0].Headers[0].Key, headerNewKey)
	is.Equal(decodeStr(t, msgs[0].Headers[0].Value), newKeyHeader)

	// tombstone: old key, null value, same header
	is.Equal(*msgs[1].Key, *msgs[0].Key)
	is.Equal(msgs[1].Value, nil)
	is.Equal(decodeStr(t, msgs[1].Headers[0].Value), newKeyHeader)

	// create: new key, header __debezium.oldkey = old key
	is.Equal(decodeStr(t, msgs[2].Key), newKey)
	var create map[string]any
	err = json.Unmarshal([]byte(decodeStr(t, msgs[2].Value)), &create)
	is.NoErr(err)
	is.Equal(create["payload"].(map[string]any)["op"], "c")
	is.Equal(msgs[2].Headers[0].Key, headerOldKey)
	is.Equal(decodeStr(t, msgs[2].Headers[0].Value), oldKeyHeader)
}

func TestSchemasDisabledOmitsSchemaWrapper(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	rec := opencdc.Record{
		Key:       opencdc.StructuredData{"seq": int32(1), "region": "eu"},
		Payload:   opencdc.Change{After: opencdc.StructuredData{"region": "eu", "seq": int32(1), "note": "first"}},
		Metadata:  compositeMetadata("r", ""),
		Operation: opencdc.OperationSnapshot,
	}
	msgs := encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, false, true))
	is.Equal(len(msgs), 1)

	key := decodeStr(t, msgs[0].Key)
	is.Equal(key, `{"seq":1,"region":"eu"}`)

	var val map[string]any
	err := json.Unmarshal([]byte(decodeStr(t, msgs[0].Value)), &val)
	is.NoErr(err)
	_, hasSchema := val["schema"]
	is.Equal(hasSchema, false)
	is.Equal(val["op"], "r")
}

func TestProcessorRejectsAvro(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	cfg := map[string]string{
		"key.converter":   "avro",
		"value.converter": "avro",
	}
	err := p.Configure(t.Context(), cfg)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeAvroUnsupported)
}

func TestProcessorDescriptorMissing(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	err := p.Configure(t.Context(), map[string]string{"key.converter": "json", "value.converter": "json"})
	is.NoErr(err)

	rec := opencdc.Record{Metadata: opencdc.Metadata{}}
	_, err = p.processRecord(rec)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeDescriptorMissing)
}
