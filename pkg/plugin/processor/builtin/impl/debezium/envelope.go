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
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/goccy/go-json"
)

// The Debezium source and transaction block names, fixed for every table.
const (
	sourceName      = "io.debezium.connector.postgresql.Source"
	transactionName = "event.block"
	enumName        = "io.debezium.data.Enum"
)

// snapshotAllowed maps a compat level to the "allowed" set of the snapshot
// enum (the JsonConverter writes the same parameters as the Avro converter).
var snapshotAllowed = map[string]string{
	Compat26: "true,last,false,incremental",
	Compat35: "true,first,first_in_data_collection,last_in_data_collection,last,false,incremental",
}

// sourceSchema returns the fixed source struct schema for a compat level.
// Field order, optionality, names and the snapshot enum's allowed set match
// the goldens (2.7.4 → compat 2.6, 3.7.0 → compat 3.5).
func sourceSchema(compat string) *schema {
	s := &schema{
		Type: typeStruct,
		Fields: []field{
			reqStr(srcVersion),
			reqStr(srcConnector),
			reqStr(srcName),
			reqInt64(srcTSMs),
			snapshotField(compat),
			reqStr(srcDB),
			optStr(srcSequence),
			optInt64(srcTSUs),
			optInt64(srcTSNs),
			reqStr(srcSchema),
			reqStr(srcTable),
			optInt64(srcTxID),
			optInt64(srcLSN),
			optInt64(srcXmin),
		},
		Name: sourceName,
	}
	if compat == Compat35 {
		s.Version = 1
		s.Fields = append(s.Fields, optStr(srcOrigin), optInt64(srcOriginLSN))
	}
	return s
}

func snapshotField(compat string) field {
	return field{Name: srcSnapshot, Schema: &schema{
		Type:     typeString,
		Optional: true,
		Name:     enumName,
		Version:  1,
		Params:   []parameter{{Key: "allowed", Value: snapshotAllowed[compat]}},
		Default:  json.RawMessage(`"false"`),
	}}
}

func transactionSchema() *schema {
	return &schema{
		Type:     typeStruct,
		Optional: true,
		Name:     transactionName,
		Version:  1,
		Fields: []field{
			reqStr("id"),
			reqInt64("total_order"),
			reqInt64("data_collection_order"),
		},
	}
}

// envelopeSchema builds the envelope schema by wrapping the descriptor's value
// schema in before/after and appending the source, transaction, op and
// timestamp fields.
func envelopeSchema(value *schema, compat string) *schema {
	before := value.clone()
	before.Field = "before"
	after := value.clone()
	after.Field = "after"

	envelopeName := strings.TrimSuffix(value.Name, ".Value") + ".Envelope"
	return &schema{
		Type:     typeStruct,
		Version:  2,
		Name:     envelopeName,
		Optional: false,
		Fields: []field{
			{Name: "before", Schema: before},
			{Name: "after", Schema: after},
			{Name: "source", Schema: sourceSchema(compat)},
			{Name: "transaction", Schema: transactionSchema()},
			reqStr("op"),
			optInt64("ts_ms"),
			optInt64("ts_us"),
			optInt64("ts_ns"),
		},
	}
}

// reqStr/optStr/reqInt64/optInt64 build simple schema fields.
func reqStr(name string) field {
	return field{Name: name, Schema: &schema{Type: typeString, Optional: false}}
}

func optStr(name string) field {
	return field{Name: name, Schema: &schema{Type: typeString, Optional: true}}
}

func reqInt64(name string) field {
	return field{Name: name, Schema: &schema{Type: typeInt64, Optional: false}}
}

func optInt64(name string) field {
	return field{Name: name, Schema: &schema{Type: typeInt64, Optional: true}}
}

// sourcePayload reads the debezium.source.* metadata into the source struct
// values. xmin, origin and origin_lsn are never emitted by the source and stay
// null, matching Debezium (xmin.fetch.interval.ms is unsupported).
func sourcePayload(md opencdc.Metadata) (map[string]any, error) {
	out := map[string]any{
		srcVersion:   md[metaSourcePfx+srcVersion],
		srcConnector: md[metaSourcePfx+srcConnector],
		srcName:      md[metaSourcePfx+srcName],
		srcTSMs:      parseInt64(md[metaSourcePfx+srcTSMs]),
		srcSnapshot:  md[metaSourcePfx+srcSnapshot],
		srcDB:        md[metaSourcePfx+srcDB],
		srcSequence:  md[metaSourcePfx+srcSequence],
		srcTSUs:      parseInt64(md[metaSourcePfx+srcTSUs]),
		srcTSNs:      parseInt64(md[metaSourcePfx+srcTSNs]),
		srcSchema:    md[metaSourcePfx+srcSchema],
		srcTable:     md[metaSourcePfx+srcTable],
		srcTxID:      parseInt64(md[metaSourcePfx+srcTxID]),
		srcLSN:       parseInt64(md[metaSourcePfx+srcLSN]),
		srcXmin:      nil,
		srcOrigin:    optionalString(md[metaSourcePfx+srcOrigin]),
		srcOriginLSN: parseInt64(md[metaSourcePfx+srcOriginLSN]),
	}
	for _, f := range []string{srcVersion, srcConnector, srcName, srcDB, srcSchema, srcTable} {
		if s, ok := out[f].(string); !ok || s == "" {
			return nil, newCoded(CodeDescriptorMissing,
				"record has no debezium.source."+f+" metadata",
				"Run this processor only on records produced by the Postgres source in debezium mode.")
		}
	}
	if out[srcTSMs] == nil {
		return nil, newCoded(CodeDescriptorMissing,
			"record has no debezium.source.ts_ms metadata",
			"Run this processor only on records produced by the Postgres source in debezium mode.")
	}
	return out, nil
}

func parseInt64(s string) any {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return n
}

// optionalString returns s, or nil when it is empty, so an optional source
// field (origin) stays null unless the source emitted it.
func optionalString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// transactionPayload reads the debezium.tx.* metadata, or returns nil when
// provide.transaction.metadata was off.
func transactionPayload(md opencdc.Metadata) map[string]any {
	if md[metaTxID] == "" {
		return nil
	}
	return map[string]any{
		"id":                    md[metaTxID],
		"total_order":           parseInt64(md[metaTxTotal]),
		"data_collection_order": parseInt64(md[metaTxCollOrder]),
	}
}

// writeEnvelopePayload writes the envelope payload (before, after, source,
// transaction, op, ts_ms, ts_us, ts_ns) against the descriptor's value schema
// and the compat source schema. The timestamps are the encode wall clock (D1:
// volatile). before and after are the per-message row values (nil = null).
func writeEnvelopePayload(b *bytes.Buffer, before, after any, value, srcSchema *schema, src map[string]any, tx map[string]any, op, decimalFormat string, nowMillis, nowMicro, nowNano int64) error {
	b.WriteString(`{"before":`)
	if before == nil {
		b.WriteString("null")
	} else if err := writeValue(b, value, before, decimalFormat); err != nil {
		return err
	}
	b.WriteString(`,"after":`)
	if after == nil {
		b.WriteString("null")
	} else if err := writeValue(b, value, after, decimalFormat); err != nil {
		return err
	}
	b.WriteString(`,"source":`)
	if err := writeValue(b, srcSchema, src, decimalFormat); err != nil {
		return err
	}
	b.WriteString(`,"transaction":`)
	if tx == nil {
		b.WriteString("null")
	} else if err := writeValue(b, transactionSchema(), tx, decimalFormat); err != nil {
		return err
	}
	b.WriteString(`,"op":`)
	writeJSONString(b, op)
	b.WriteString(`,"ts_ms":`)
	b.WriteString(strconv.FormatInt(nowMillis, 10))
	b.WriteString(`,"ts_us":`)
	b.WriteString(strconv.FormatInt(nowMicro, 10))
	b.WriteString(`,"ts_ns":`)
	b.WriteString(strconv.FormatInt(nowNano, 10))
	b.WriteByte('}')
	return nil
}
