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

package smtoracle

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

// Fixture file names in fixtures/.
const (
	// DebeziumFile holds what real Debezium Postgres wrote to Kafka for the
	// fixture database, as oracle input records.
	DebeziumFile = "debezium-postgres.json"
	// DebeziumDefaultConverterFile is the same capture with the worker's
	// converters at their defaults (replace.null.with.default=true).
	DebeziumDefaultConverterFile = "debezium-postgres-default-converter.json"
	// ConduitFile holds what Conduit's built-in Postgres source produced for the
	// same database in the same capture run: a JSON array of OpenCDC records.
	ConduitFile = "conduit-postgres.json"
	// SchemasFile holds every schema Conduit's source registered, in
	// registration order per subject.
	SchemasFile = "conduit-schemas.json"
)

// Fixture returns the raw bytes of a file in fixtures/.
func Fixture(name string) ([]byte, error) {
	b, err := files.ReadFile(path.Join("fixtures", name))
	if err != nil {
		return nil, fmt.Errorf("smtoracle: %w", err)
	}
	return b, nil
}

// DebeziumEvents returns the captured Debezium messages: the key and value
// payloads and schemas as the JsonConverter wrote them, one InputRecord per
// Kafka message in topic and offset order. A delete is followed by its
// tombstone (a record whose Value is JSON null).
func DebeziumEvents() ([]InputRecord, error) {
	b, err := Fixture(DebeziumFile)
	if err != nil {
		return nil, err
	}
	var recs []InputRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("smtoracle: decode %s: %w", DebeziumFile, err)
	}
	return recs, nil
}

// Schema is one registered Conduit schema.
type Schema struct {
	Subject string `json:"subject"`
	Version int    `json:"version"`
	// Type is "AVRO", "PROTOBUF" or "JSON".
	Type   string `json:"type"`
	Schema string `json:"schema"`
}

// Schemas returns the schemas behind the Conduit records, in the order they
// must be registered to get the same versions.
func Schemas() ([]Schema, error) {
	b, err := Fixture(SchemasFile)
	if err != nil {
		return nil, err
	}
	var ss []Schema
	if err := json.Unmarshal(b, &ss); err != nil {
		return nil, fmt.Errorf("smtoracle: decode %s: %w", SchemasFile, err)
	}
	return ss, nil
}

// ConduitRecord is the part of an OpenCDC record the pairing needs. The full
// record is decoded by the replay package.
type ConduitRecord struct {
	Operation string            `json:"operation"`
	Metadata  map[string]string `json:"metadata"`
	Key       json.RawMessage   `json:"key"`
}

// ConduitRecords returns the captured OpenCDC records in file order
// (grouped by collection, arrival order within it), reduced to the pairing
// fields.
func ConduitRecords() ([]ConduitRecord, error) {
	b, err := Fixture(ConduitFile)
	if err != nil {
		return nil, err
	}
	var recs []ConduitRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("smtoracle: decode %s: %w", ConduitFile, err)
	}
	return recs, nil
}

// Collection returns the table a Conduit record came from.
func (r ConduitRecord) Collection() string { return r.Metadata["opencdc.collection"] }

// Pair links one Debezium message with the Conduit record for the same
// change of the same row.
type Pair struct {
	Table string
	// Op is the Conduit operation: snapshot, create, update or delete.
	Op string
	// Debezium is the Name of the Debezium input record (the change event,
	// not its tombstone).
	Debezium string
	// Conduit indexes ConduitRecords().
	Conduit int
	// Tombstone is the Name of the Debezium tombstone that follows a delete,
	// or "". Conduit emits no tombstone.
	Tombstone string
}

const opDelete = "delete"

var debeziumOps = map[string]string{"r": "snapshot", "c": "create", "u": "update", "d": opDelete}

// Pairs matches every Debezium change event with the Conduit record for it:
// per table, in order, with the same operation and the same primary key.
// It fails when the two captures disagree on what happened, which is how a
// fixture that was captured from two different workloads is caught offline.
func Pairs() ([]Pair, error) {
	dbz, err := DebeziumEvents()
	if err != nil {
		return nil, err
	}
	cond, err := ConduitRecords()
	if err != nil {
		return nil, err
	}
	byTable := map[string][]int{}
	for i, r := range cond {
		byTable[r.Collection()] = append(byTable[r.Collection()], i)
	}
	next := map[string]int{}
	var pairs []Pair
	for i, ev := range dbz {
		if string(ev.Value) == "null" || len(ev.Value) == 0 {
			if len(pairs) == 0 || pairs[len(pairs)-1].Op != opDelete {
				return nil, fmt.Errorf("smtoracle: tombstone %s does not follow a delete", ev.Name)
			}
			pairs[len(pairs)-1].Tombstone = ev.Name
			continue
		}
		table := ev.Topic[strings.LastIndex(ev.Topic, ".")+1:]
		var env struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(ev.Value, &env); err != nil {
			return nil, fmt.Errorf("smtoracle: %s: %w", ev.Name, err)
		}
		op, ok := debeziumOps[env.Op]
		if !ok {
			return nil, fmt.Errorf("smtoracle: %s has op %q", ev.Name, env.Op)
		}
		idx := byTable[table]
		if next[table] >= len(idx) {
			return nil, fmt.Errorf("smtoracle: %s (event %d) has no Conduit record: table %s has %d", ev.Name, i, table, len(idx))
		}
		ci := idx[next[table]]
		next[table]++
		c := cond[ci]
		if c.Operation != op {
			return nil, fmt.Errorf("smtoracle: %s is %q in Debezium but the matching Conduit record %d is %q", ev.Name, op, ci, c.Operation)
		}
		if d := Diff(ev.Key, c.Key); d != "" {
			return nil, fmt.Errorf("smtoracle: %s and Conduit record %d have different keys: %s", ev.Name, ci, d)
		}
		pairs = append(pairs, Pair{Table: table, Op: op, Debezium: ev.Name, Conduit: ci})
	}
	for table, idx := range byTable {
		if next[table] != len(idx) {
			return nil, fmt.Errorf("smtoracle: table %s has %d Conduit records and %d Debezium changes", table, len(idx), next[table])
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Conduit < pairs[j].Conduit })
	return pairs, nil
}
