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

// Package debezium builds Debezium change events from records produced by a
// Postgres source running in Debezium-compatible mode. It is the core of the
// debezium.encode processor described in
// docs/design-documents/20261012-debezium-compatible-output.md.
//
// The input is one record per WAL change in the shape the design fixes
// (Record shape between source and processor): Debezium-typed values in
// Key and Payload, and debezium.* metadata carrying the topic, the op, the
// source block, the transaction block, the old key of a primary-key update
// and the descriptor. The descriptor holds the row's key and value schemas in
// Kafka Connect JSON schema form (see [ParseDescriptor]); the source decides
// every type, this package only assembles.
//
// The output is the message plan Debezium produces for that WAL change
// ([Plan]): one message for an insert, update or snapshot read; a delete plus
// a tombstone for a delete; delete, tombstone and create for a primary-key
// change; one message for a logical decoding message. Each message holds
// Connect schemas and values; [Message.Encode] serializes them with the
// converters Kafka Connect would use.
//
// What this package does not do: talk to a Schema Registry, frame a plan into
// a record for the Kafka destination, or decide anything about types. Those
// are the processor's and the source's jobs.
//
// Invariants enforced here:
//
//   - Invariant 6: every record value is checked against the descriptor. An
//     unknown field, a missing required field, a source-block key the schema
//     does not have, or a value of the wrong Go type fails the record with a
//     stable code (connect.CodeRecordSchemaMismatch and friends). Nothing is
//     dropped or coerced.
//   - Invariant 3/4: a delete always yields its tombstone (when
//     tombstones.on.delete is on) in the same plan, after the delete, with the
//     same key, so the two cannot be split by acknowledgement and share a
//     partition. A primary-key change yields its three messages in Debezium's
//     order.
//
// The golden files in tests/debezium/testdata/golden, captured from real
// Debezium, are the specification; golden_test.go rebuilds every captured
// event from a record and compares the bytes.
package debezium
