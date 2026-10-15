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

// Package starthook holds the checks both pipeline engines (pkg/lifecycle and
// pkg/lifecycle-poc) run when a pipeline starts, before anything is built.
//
// [Check] reads the stored pipeline: its connectors and processors, in the
// order records flow through them, and for each plugin reference whether the
// plugin services would resolve it to a standalone plugin. It reads the
// registries' state and dispenses nothing, so it opens no connector, starts no
// plugin process and builds no WASM module. A refusal is a coded
// conduiterr.ConduitError; nothing has been read from a source, so nothing is
// acked (invariants 1 and 3).
//
// Both engines call Check in startRun, which serves Start (from the API, from
// provisioning, and from Init at boot) and the recovery restart. A refusal on
// Start is returned to the caller and the pipeline does not start. A refusal
// on a recovery restart is the recovery's error: the pipeline goes to degraded
// and is not retried, because the configuration will not change by itself.
//
// startRun is not the only place a running pipeline's processors change: the
// classic engine's live in-place swap (pkg/lifecycle ReconfigureProcessor,
// used by provisioning's live apply) builds a new processor without a
// restart, so it calls Check too, after the new config is stored and before
// the new processor is built. A refusal there keeps the old processor running
// and is returned to the caller, which rolls the stored config back. The
// arch-v2 engine has no live swap; it falls back to a restart, which goes
// through startRun.
//
// A check applies only when the pipeline contains what it guards; for any
// other pipeline Check resolves no plugin and returns nil.
//
// Checks today:
//
//   - Debezium plugin boundary (CodeDebeziumStandaloneBeforeEncode): records
//     that reach a debezium.encode processor must not have crossed the plugin
//     protocol. See docs/design-documents/20261012-debezium-compatible-output.md,
//     decision 18.
//
// The other debezium.encode placement rules in that design (last on the path,
// no condition, plan-capable destinations, halting DLQ) belong here too and
// are not implemented yet.
package starthook
