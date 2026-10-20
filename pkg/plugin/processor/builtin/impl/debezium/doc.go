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

// Package debezium implements the debezium.encode built-in processor, the
// envelope / naming / tombstone half of Conduit's Debezium-compatible output
// mode for Postgres CDC (issue #3032, design
// docs/design-documents/20261012-debezium-compatible-output.md).
//
// The Postgres source's debezium mode emits one OpenCDC record per WAL change
// carrying Debezium-typed values (Key, Payload.Before and Payload.After),
// debezium.* metadata (topic, op, the source block, an in-record Connect
// schema descriptor). This processor turns each such record into the messages
// Debezium would have produced:
//
//   - an insert or snapshot read becomes one create/read message;
//   - an update becomes one update message, or, when debezium.old_key is set,
//     a delete, a tombstone and a create (a primary-key change);
//   - a delete becomes a delete message and, with tombstones.on.delete, a
//     tombstone (same key, null value).
//
// Each message carries its serialized key, value and headers, and the ordered
// list is written as a versioned frame into Payload.After with
// kafka.messages.version=1. The Kafka destination produces every message of
// the frame and acknowledges the record only when all of them succeeded
// (invariant 1), so positions, change keys and the source's flush gate stay
// one-record-per-change (invariant 2).
//
// This revision implements the JsonConverter (schemas.enable true and false,
// decimal.format BASE64 and NUMERIC). The Confluent AvroConverter (AvroData
// port, subject naming, Confluent wire format) is the design's default and is
// the next revision; selecting avro fails at configure time with
// debezium.avro_unsupported rather than emitting half-encoded messages.
//
// Two contract decisions the design left to the implementation, resolved
// here:
//
//   - How the encoder learns the Debezium compat boundary: the source-schema
//     shape (snapshot enum "allowed" set, the source schema .version, and the
//     origin/origin_lsn fields) is fixed per boundary, so the encoder takes a
//     debezium.compat setting that migrate sets identically on the source and
//     the encoder. It defaults to the latest boundary (3.5).
//
//   - The message-plan frame format (undefined in the design): a JSON object
//     {"version":1,"messages":[...]} with per-message topic, key, value and
//     headers, where key and value are base64 strings and null distinguishes
//     an absent key from an empty one and a tombstone from an empty value.
package debezium
