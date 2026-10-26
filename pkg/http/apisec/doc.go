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

// Package apisec holds the access controls for Conduit's network API: the
// bearer-token gate for HTTP, gRPC and the inspector WebSocket, and the
// browser guard (Origin, Host and Content-Type checks) that stops a web page
// from driving a locally or privately reachable Conduit instance.
//
// # Invariants
//
//   - A request that reaches a gated handler either presented the configured
//     token (compared in constant time) or hit one of the few documented
//     exemptions (liveness/readiness probes, CORS preflights, the gRPC health
//     check).
//   - A browser request whose Origin is neither allowlisted nor same-origin
//     never reaches the API, on any method, including OPTIONS and WebSocket
//     upgrades.
//   - On a loopback bind the Host header is validated before the same-origin
//     comparison is trusted, so DNS rebinding cannot turn an attacker origin
//     into a "same-origin" one.
//   - Every refusal carries a stable error code (see codes.go), the config
//     path that controls it and a suggested fix.
//
// The package is deliberately free of runtime wiring; pkg/conduit decides
// which listeners get which gates.
package apisec
