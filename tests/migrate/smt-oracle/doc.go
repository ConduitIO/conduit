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

// Package smtoracle loads the Kafka Connect and Debezium SMT output captured
// by the program in this directory (capture.sh, java/Oracle.java).
//
// The captured files are the oracle for the kcmigrate SMT translators: a
// translator corpus case runs the processors it emits over the same input
// records as a suite in cases/ and compares each result with the golden for
// that (suite, run, record). The package only reads and compares; it never
// runs a transform, and it needs no JVM.
//
// A golden file holds one Suite. A Suite has Runs (one SMT chain each), and
// a Run has one Result per input record, in input order. Schemas are stored
// once per suite in Suite.Schemas and referenced by id from Side.Schema.
// Schemaless sides carry Java types JSON cannot tell apart as single-key
// tagged objects ({"$int32": 5}, {"$date": "..."}, {"$bytes": "..."}); see
// the README for the full list.
package smtoracle
