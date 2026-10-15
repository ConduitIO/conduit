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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
)

// Compat is the debezium.compat setting: the Debezium version range whose
// output shape is reproduced. Only the boundaries verified against a golden
// capture are accepted; the design's other boundaries ("pre-2.6", "3.0.5")
// are refused until the harness has an image for them, rather than guessed.
type Compat string

const (
	// Compat26 is 2.6.0.Beta1 up to 3.0.4. Verified against Debezium 2.7.4.
	Compat26 Compat = "2.6"
	// Compat35 is 3.5 and later. Verified against Debezium 3.7.0.
	Compat35 Compat = "3.5"
)

// ParseCompat validates a debezium.compat value.
func ParseCompat(s string) (Compat, error) {
	switch Compat(s) {
	case Compat26, Compat35:
		return Compat(s), nil
	case "pre-2.6", "3.0.5":
		return "", conduiterr.New(connect.CodeConfigInvalid, fmt.Sprintf(
			"debezium.compat %q is in the design but not yet verified against a golden capture; use %q or %q",
			s, Compat26, Compat35,
		))
	}
	return "", conduiterr.New(connect.CodeConfigInvalid, fmt.Sprintf("debezium.compat %q is not one of %q, %q", s, Compat26, Compat35))
}

func intp(i int) *int { return &i }
func opt(t connect.Type) *connect.Schema {
	return &connect.Schema{Type: t, Optional: true}
}

func req(t connect.Type) *connect.Schema {
	return &connect.Schema{Type: t}
}

// snapshotAllowed is the "allowed" parameter of source.snapshot. Conduit only
// produces "true" and "false" (design D4), but the schema lists every value
// the targeted Debezium version declares, so the schema is Debezium's.
func snapshotAllowed(c Compat) string {
	if c == Compat26 {
		return "true,last,false,incremental"
	}
	return "true,first,first_in_data_collection,last_in_data_collection,last,false,incremental"
}

// sourceSchema is io.debezium.connector.postgresql.Source for a compat level,
// field for field as the goldens show.
func sourceSchema(c Compat) *connect.Schema {
	s := &connect.Schema{
		Type: connect.TypeStruct,
		Name: "io.debezium.connector.postgresql.Source",
		Fields: []connect.Field{
			{Name: "version", Schema: req(connect.TypeString)},
			{Name: "connector", Schema: req(connect.TypeString)},
			{Name: "name", Schema: req(connect.TypeString)},
			{Name: "ts_ms", Schema: req(connect.TypeInt64)},
			{Name: "snapshot", Schema: &connect.Schema{
				Type: connect.TypeString, Optional: true, Name: "io.debezium.data.Enum", Version: intp(1),
				Parameters: []connect.Param{{Key: "allowed", Value: snapshotAllowed(c)}},
				Default:    "false", HasDefault: true,
			}},
			{Name: "db", Schema: req(connect.TypeString)},
			{Name: "sequence", Schema: opt(connect.TypeString)},
			{Name: "ts_us", Schema: opt(connect.TypeInt64)},
			{Name: "ts_ns", Schema: opt(connect.TypeInt64)},
			{Name: "schema", Schema: req(connect.TypeString)},
			{Name: "table", Schema: req(connect.TypeString)},
			{Name: "txId", Schema: opt(connect.TypeInt64)},
			{Name: "lsn", Schema: opt(connect.TypeInt64)},
			{Name: "xmin", Schema: opt(connect.TypeInt64)},
		},
	}
	if c == Compat35 {
		s.Version = intp(1)
		s.Fields = append(
			s.Fields,
			connect.Field{Name: "origin", Schema: opt(connect.TypeString)},
			connect.Field{Name: "origin_lsn", Schema: opt(connect.TypeInt64)},
		)
	}
	return s
}

// transactionSchema is the envelope's transaction block (Avro name
// event.block). It is in every envelope schema; its value is null unless the
// source provides transaction metadata (design D12).
var transactionSchema = &connect.Schema{
	Type: connect.TypeStruct, Optional: true, Name: "event.block", Version: intp(1),
	Fields: []connect.Field{
		{Name: "id", Schema: req(connect.TypeString)},
		{Name: "total_order", Schema: req(connect.TypeInt64)},
		{Name: "data_collection_order", Schema: req(connect.TypeInt64)},
	},
}

// envelopeSchema is <base>.Envelope version 2 around a row's value schema.
func envelopeSchema(base string, value *connect.Schema, c Compat) *connect.Schema {
	return &connect.Schema{
		Type: connect.TypeStruct, Name: base + ".Envelope", Version: intp(2),
		Fields: []connect.Field{
			{Name: "before", Schema: value},
			{Name: "after", Schema: value},
			{Name: "source", Schema: sourceSchema(c)},
			{Name: "transaction", Schema: transactionSchema},
			{Name: "op", Schema: req(connect.TypeString)},
			{Name: "ts_ms", Schema: opt(connect.TypeInt64)},
			{Name: "ts_us", Schema: opt(connect.TypeInt64)},
			{Name: "ts_ns", Schema: opt(connect.TypeInt64)},
		},
	}
}

// Logical decoding message schemas (op "m"). The value has no before/after
// and, unlike row envelopes, no ts_us/ts_ns; from the 3.5 boundary it gains
// the transaction block.
var messageKeySchema = &connect.Schema{
	Type: connect.TypeStruct, Name: "io.debezium.connector.postgresql.MessageKey", Version: intp(1),
	Fields: []connect.Field{{Name: "prefix", Schema: opt(connect.TypeString)}},
}

var messageBlockSchema = &connect.Schema{
	Type: connect.TypeStruct, Name: "io.debezium.connector.postgresql.Message", Version: intp(1),
	Fields: []connect.Field{
		{Name: "prefix", Schema: opt(connect.TypeString)},
		{Name: "content", Schema: opt(connect.TypeBytes)},
	},
}

func messageValueSchema(c Compat) *connect.Schema {
	s := &connect.Schema{
		Type: connect.TypeStruct, Name: "io.debezium.connector.postgresql.MessageValue", Version: intp(1),
		Fields: []connect.Field{
			{Name: "op", Schema: req(connect.TypeString)},
			{Name: "ts_ms", Schema: opt(connect.TypeInt64)},
			{Name: "source", Schema: sourceSchema(c)},
		},
	}
	if c == Compat35 {
		s.Fields = append(s.Fields, connect.Field{Name: "transaction", Schema: transactionSchema})
	}
	s.Fields = append(s.Fields, connect.Field{Name: "message", Schema: messageBlockSchema})
	return s
}
