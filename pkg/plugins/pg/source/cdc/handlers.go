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

package cdc

import (
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/jackc/pgx/pgtype"
)

// handleInsert formats a Record with INSERT event data from Postgres and
// inserts it into the records buffer for later reading.
func (i *Iterator) handleInsert(
	relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := sdk.Record{
		CreatedAt: time.Now(),
		Metadata: map[string]string{
			"action": "insert",
			"table":  i.config.TableName,
		},
	}
	rec = i.withKey(rec, values)
	rec = i.withPosition(rec, int64(pos))
	rec = i.withPayload(rec, values)
	i.Push(rec)
	return nil
}

// handleUpdate formats a record with a UPDATE event data from Postgres and
// inserts it into the records buffer for later reading.
func (i *Iterator) handleUpdate(
	relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := sdk.Record{
		// TODO: Fill out key and add payload and metadata
		CreatedAt: time.Now(),
		Metadata: map[string]string{
			"action": "update",
			"table":  i.config.TableName,
		},
	}
	rec = i.withKey(rec, values)
	rec = i.withPosition(rec, int64(pos))
	rec = i.withPayload(rec, values)
	i.Push(rec)
	return nil
}

// handleDelete formats a record with a delete event data from Postgres.
// delete events only send along the primary key of the table.
func (i *Iterator) handleDelete(
	relID pgtype.OID,
	values map[string]pgtype.Value,
	pos uint64,
) error {
	rec := sdk.Record{
		CreatedAt: time.Now(),
		Metadata: map[string]string{
			"action": "delete",
			"table":  i.config.TableName,
		},
	}
	rec = i.withKey(rec, values)
	rec = i.withPosition(rec, int64(pos))
	// NB: Delete's shouldn't have payloads. Key + delete action is sufficient.
	i.Push(rec)
	return nil
}

// withKey takes the values from the message and extracts a key that matches
// the configured keyColumnName.
func (i *Iterator) withKey(rec sdk.Record, values map[string]pgtype.Value) sdk.Record {
	key := sdk.StructuredData{}
	for k, v := range values {
		if i.config.KeyColumnName == k {
			key[k] = v.Get()
			rec.Key = key
		}
	}
	return rec
}

// withPayload takes a record and a map of values and formats a payload for
// the record and then returns the record with that payload attached.
func (i *Iterator) withPayload(rec sdk.Record, values map[string]pgtype.Value) sdk.Record {
	rec.Payload = i.formatPayload(values)
	return rec
}

// formatPayload formats a structured data payload from a map of Value types.
func (i *Iterator) formatPayload(values map[string]pgtype.Value) sdk.StructuredData {
	payload := sdk.StructuredData{}
	for k, v := range values {
		value := v.Get()
		payload[k] = value
	}
	delete(payload, i.config.KeyColumnName) // NB: dedupe Key out of payload
	return payload
}

// sets an integer position to the correct stringed integer on
func (i *Iterator) withPosition(rec sdk.Record, pos int64) sdk.Record {
	rec.Position = sdk.Position(strconv.FormatInt(pos, 10))
	// TODO: update iterator's last seen position
	return rec
}
