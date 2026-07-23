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

// Package chaos is the repo's first chaos-testing harness (v0.19 Workstream 7,
// DBZ-1). It is deliberately minimal — sized for one scenario family (a SIGKILL
// crash-safety test on the source connector's offset↔position bridge), not a
// generalized chaos framework. If a second scenario (DBZ-2) ever needs one,
// it should generalize from this package rather than the reverse; see
// docs/design-documents (dbz1-chaos.md, the design doc this package
// implements) for why building the generalized skeleton now would be
// speculative generality.
//
// # What this package tests, and why the real Debezium wrapper isn't here
//
// The workstream's motivating question is about
// ConduitIO/conduit-kafka-connect-wrapper (a real Debezium Postgres connector
// running inside Conduit): does its ack->commit ordering survive a `kill -9`
// mid-snapshot or mid-stream without losing or duplicating records? That
// wrapper is a separate JVM repo with a real Postgres dependency, and is not
// checked out in this repository's module — it cannot be driven from a Go
// test here.
//
// What CAN be tested here, with full fidelity, is the Go engine code the
// wrapper (and every other v1-protocol, out-of-process connector) depends on:
// pkg/connector.Source.Ack and pkg/connector.Persister. Reading
// pkg/connector/source.go:207-238 shows the exact sequence a crash can land
// in the middle of:
//
//  1. Source.Ack sends the ack to the plugin (source.go:218) — for the
//     wrapper, this is what drives Debezium's task.commitRecord/task.commit,
//     which durably (and, critically, IRREVERSIBLY from Conduit's point of
//     view) advances Postgres's replication-slot confirmed LSN.
//  2. ONLY AFTER that send succeeds does Source.Ack update its own state and
//     call persister.Persist (source.go:223-235) — and Persister.Persist
//     (persister.go:137-170) does NOT write through: it batches the change
//     and flushes asynchronously after DefaultPersisterDelayThreshold (1s) or
//     DefaultPersisterBundleCountThreshold (10k items), whichever comes
//     first.
//
// So there is a real, ~1-second (or 10k-record) window after Conduit tells a
// plugin "you may durably commit this" during which Conduit's OWN record of
// "where we are" has not yet reached durable storage. A `kill -9` inside that
// window is exactly what this package's tests target — with a plain
// wall-clock delay, not a deterministic kill-hook, because the window is wide
// enough (~1s) that timing it precisely isn't necessary.
//
// # Why "gap or duplicate" depends on the plugin, and this package tests both
//
// Whether that crash window is merely wasteful (Conduit re-delivers a few
// already-committed records as harmless duplicates — fine, at-least-once) or
// actually loses data (an unrecoverable GAP) depends entirely on whether the
// plugin's backing system can still supply data from BEFORE its own most
// recent commit. A Kafka-like log can. A Postgres replication slot, whose WAL
// segments are eligible for recycling once the slot's confirmed_flush_lsn
// advances past them, may not be able to — and real Postgres surfaces that
// as a hard error ("requested WAL segment ... has already been removed"),
// not a silent skip.
//
// Since the real wrapper+Postgres isn't available here, sigkill_test.go
// drives BOTH assumptions against the identical engine code path, via a
// synthetic upstreamStore (upstream.go) with a `prune` flag:
//
//   - prune=false (durable/Kafka-like upstream): the crash window must
//     produce, at most, harmless duplicate re-delivery. No gap, ever.
//   - prune=true (Postgres-replication-slot-like upstream): the SAME crash
//     window, in the SAME engine code, is shown to make a GAP structurally
//     reachable — Source.Open, on restart, is asked to resume from a
//     position the upstream has already discarded.
//
// The verdict this package's tests establish is therefore conditional and
// precise, not a blanket "crash-safe" or "not crash-safe" claim: Conduit's own
// ack-before-persist ordering provides no protection on its own — safety
// depends entirely on the connected plugin's ability to redeliver behind its
// last commit, which for a real Debezium-Postgres connector is not
// guaranteed. See the PR description's failure-mode analysis for the full
// walk of the ack->commit->persist sequence and what each crash point does.
package chaos
