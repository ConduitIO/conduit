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

// Package external dispenses a connector plugin that is already running at a
// known address, instead of spawning a subprocess
// (pkg/plugin/connector/standalone's model). It is the foundational piece of
// embed Slice 2 (docs/design-documents/20260724-embed-slice2-external-connector.md,
// the gating addendum to
// docs/design-documents/20260724-embed-grpc-client-libraries.md and
// docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md):
// the mechanism inline_source/inline_destination (a host-language connector
// server running in-process inside an embedding application) is built on.
//
// # Mechanism
//
// go-plugin (github.com/hashicorp/go-plugin) supports acquiring a
// plugin.Client two ways: spawning a subprocess (plugin.ClientConfig.Cmd,
// what standalone.Dispenser uses via conduit-connector-protocol's
// pconnector/client.New) or reattaching to a process already running at a
// known address (plugin.ClientConfig.Reattach). Both converge on the exact
// same code path once a plugin.Client has an address: gRPC dial, then
// dispensing the pconnector v1/v2 gRPC client stubs. No conduit-connector-
// protocol wire message or .proto file changes between the two - Reattach
// only changes how the client finds the server, never what it says to it
// once connected. Dispenser implements the same connector.Dispenser
// interface (DispenseSpecifier/Source/Destination) as standalone.Dispenser,
// so pkg/connector's Source/Destination call sites are unaware of which one
// produced their stream.
//
// # What go-plugin does NOT do on the Reattach path (and this package must)
//
// Two behaviors verified directly against github.com/hashicorp/go-plugin
// v1.8.0 for the Slice 2 addendum change what this package builds, not just
// documents:
//
//  1. Production Reattach performs zero version-based plugin-set selection.
//     checkProtoVersion (the spawn path's handshake-line parser) never runs;
//     plugin.ClientConfig.Plugins is never derived from VersionedPlugins.
//     Dispenser therefore hard-codes a single ProtocolVersion's gRPC stub
//     set into ClientConfig.Plugins (pluginset.go) and closes the resulting
//     gap itself: Validate (validate.go) performs a live Specify round trip
//     immediately after dial and classifies failure as unreachable
//     (connector.external_unreachable) or version-incompatible
//     (connector.external_version_mismatch) before a caller wires the
//     connector into a running pipeline.
//  2. go-plugin's default Reattach death-detection (cmdrunner.ReattachFunc)
//     is local-OS-PID-based and, load-bearing for safety: its Kill()
//     ultimately calls os.Process.Kill() on that PID. That default is wrong
//     for an external connector on two counts - the PID is not guaranteed to
//     identify Conduit's peer at all, and Conduit did not spawn this
//     process and must never send it a termination signal (a coincidentally
//     reused local PID would otherwise be killed instead). reattach.go
//     supplies a custom ReattachFunc/AttachedRunner whose Kill is a
//     deliberate no-op; a dead peer is discovered the same way a crashed
//     spawned plugin already is today - the next failed Send/Recv on the
//     gRPC stream (pkg/connector/source.go, destination.go) - not via
//     proactive health-checking. This is a deliberate no-speculative-
//     generality choice, not an oversight; see reattach.go's doc comment for
//     the trade-off.
//
// # Scope of this slice
//
// This package is the dispenser only: Reattach-based client construction,
// the hard-coded plugin set, the safe custom ReattachFunc, and the
// reachability/version-check Validate step. It does not (yet) wire an
// "address:" connector into config.Connector, the connector registry, or
// CreatePipeline - that is a separate, later reviewable slice building on
// this one (see the Slice 2 addendum's own gate list and this repo's
// tracking issue for the remaining slices). Mode 2 (dialing a genuinely
// remote, non-loopback host process) is explicitly out of scope everywhere
// in this package, per the addendum's hard gate; only Mode 1
// (conduit.local(), loopback, same machine) is supported.
//
// # Invariant
//
// Dispenser and its ReattachFunc never send a termination signal to the
// process listening at the dispensed address. Conduit did not spawn it and
// cannot safely assume a local OS PID identifies it. Teardown only closes
// this package's own gRPC client connection.
package external
