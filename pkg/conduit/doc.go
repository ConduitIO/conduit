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

// Package conduit wires up everything under the hood of a Conduit instance:
// configuration, metrics, telemetry, logging, and server construction. It
// should only ever interact with the Orchestrator, never individual services;
// that responsibility is the Orchestrator's.
//
// Embedding: a program that imports this package must add
//
//	replace github.com/dop251/goja => github.com/ConduitIO/goja v0.0.0-20261020160110-aed438ab3fe6
//
// to its own go.mod until github.com/dop251/goja/pull/816 is merged and
// released. Conduit's go.mod carries the same line, but a replace only applies
// to the main module. Without it, a custom.javascript script that stores a Map
// or Set inside itself crashes the process (docs/operations/js-processor-output.md).
package conduit
