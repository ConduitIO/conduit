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

// Package enginetest is the simulated-connector test kit for the pipeline
// engines: in-process fake sources and destinations whose behaviour a test
// controls, usable by both pkg/lifecycle (v1) and pkg/lifecycle-poc (arch-v2).
//
// The fakes satisfy, structurally, the Source and Destination interfaces of
// pkg/lifecycle/stream and pkg/lifecycle-poc/funnel, so this package imports
// neither engine and can be used from their internal tests and benchmarks. The
// drivers that assemble a whole pipeline on each engine live in the engines
// sub-package.
//
// # What can be controlled
//
// Source: record count and shape, batch size per Read, read latency, a stall
// (Gate), a per-call hook that can fail or block a Read, a crash, and whether
// the source ends with io.EOF or stays open when it runs dry.
//
// Destination: write latency, ack latency, acks returned in chunks, acks
// delivered through a buffered pump (the shape of a gRPC destination), SDK-style
// batching (flush at Size records or after Delay), per-record nacks, write
// errors, ack stalls, and a crash.
//
// # No sleeps
//
// Everything a test needs to wait for is signalled: Source.WaitAcked,
// Destination.WaitDelivered, Gate. Latency fields are plain durations meant for
// benchmarks that need real elapsed time; a test that wants deterministic
// ordering leaves them zero and drives the fakes with gates and hooks instead.
//
// # Checking the delivery invariants
//
// A Ledger shared by a source and its destinations records, at the moment the
// source is acked, whether the record was already durable downstream (or in the
// DLQ). It turns invariant 1 (never ack before durably handled) into an
// assertion that fires at the offending Ack call, not after the fact.
package enginetest
