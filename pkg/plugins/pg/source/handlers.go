// Copyright © 2022 Meroxa, Inc.
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

package source

import (
	"log"
	"time"

	"github.com/conduitio/conduit/pkg/record"
	"github.com/jackc/pgx/pgtype"
)

// handleInsert formats a Record with INSERT event data from Postgres and
// inserts it into the records buffer for later reading.
func (s *Source) handleInsert(relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := record.Record{
		CreatedAt: time.Now(),
		Metadata: map[string]string{
			"action": "insert",
			"table":  s.table,
		},
	}
	// assign a position
	rec = withPosition(rec, int64(pos))
	// build a payload from values
	rec = s.withValues(rec, values)
	// push it into the channel and return
	s.cdc.Push(rec)
	return nil
}

// handleUpdate formats a record with a UPDATE event data from Postgres and
// inserts it into the records buffer for later reading.
func (s *Source) handleUpdate(
	relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := record.Record{
		// TODO: Fill out key and add payload and metadata
		CreatedAt: time.Now(),
		Metadata: map[string]string{
			"action": "update",
			"table":  s.table,
		},
		Payload: record.StructuredData{},
	}
	// assign a position
	rec = withPosition(rec, int64(pos))
	// build a payload from values
	rec = s.withValues(rec, values)
	// push it into the channel and return
	s.cdc.Push(rec)
	return nil
}

// handleDelete formats a record with DELETE event data from Postgres and
// inserts it into the records buffer for later reading.
func (s *Source) handleDelete(
	relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := record.Record{
		Metadata: map[string]string{
			"action": "delete",
			"table":  s.table,
		},
	}
	// assign a position
	rec = withPosition(rec, int64(pos))
	// build a payload from values
	rec = s.withValues(rec, values)

	// push it into the channel and return nil
	s.cdc.Push(rec)
	return nil
}

// withValues takes a record and a map of values and formats a payload for
// the record and then returns that a record.
// * It will specifically
func (s *Source) withValues(
	rec record.Record,
	values map[string]pgtype.Value,
) record.Record {
	payload := make(record.StructuredData)
	for k, v := range values {
		// assign a key first because all records need a key and our payload
		// filter will ignore the key if it's not a specified payload field.
		if k == s.key {
			// if this column matches our key column setting,
			// then we need to set the record.Key to this value
			b := make([]byte, 0)
			if err := v.AssignTo(&b); err != nil {
				log.Printf("withValues failed to assign key: %s", err)
			}
			rec.Key = record.StructuredData{
				s.key: string(b),
			}
		}

		// skip this key if it's not a column in `s.columns`
		if exists := contains(s.columns, k); !exists {
			continue
		}

		switch val := v.(type) {
		// NB: we handle all integer values at int64 since that's how row
		// queries handle it.
		case *pgtype.Int4:
			payload[k] = int64(val.Int)
		case *pgtype.Int8:
			payload[k] = val.Int
		case *pgtype.Float4:
			payload[k] = val.Float
		case *pgtype.Float8:
			payload[k] = val.Float
		case *pgtype.Bool:
			payload[k] = val.Bool
		case *pgtype.Varchar:
			payload[k] = val.String
		case *pgtype.Bytea:
			payload[k] = string(val.Bytes)
		case *pgtype.Date:
			payload[k] = val.Time
		case *pgtype.JSON:
			payload[k] = string(val.Bytes)
		case *pgtype.JSONB:
			payload[k] = string(val.Bytes)
		case *pgtype.UUID:
			// NB: trick to convert [16]byte into []byte with slice expression
			b := val.Bytes
			var u = b[:]
			payload[k] = string(u)
		case *pgtype.Timestamptz:
			payload[k] = val.Time
		case *pgtype.Timestamp:
			payload[k] = val.Time
		case *pgtype.Text:
			payload[k] = val.String
		default:
			log.Printf("failed to find handler for %+v - type: %T", v, v)
		}
	}
	rec.Payload = payload
	return rec
}

// contains is a helper function for detecting if a column name exists in a
// slice of column names.
func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}
