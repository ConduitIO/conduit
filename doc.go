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

// Package conduit is Conduit's embeddable Go API — the frozen, semver-committed
// import path a Go application uses to run a Conduit engine in-process, without
// touching os.Exit, a global logger, or the process-wide default Prometheus
// registry.
//
// # Scope (v1 / "B1" + "B2")
//
// This package covers the engine lifecycle: New constructs an Engine from
// Options (and eagerly opens its database — see New's and Engine.Close's doc);
// Engine.Run starts it and returns a *Handle once it is ready to accept work;
// Handle.Stop drains and shuts it down; Engine.Close releases the resources
// New acquired, whether or not Run was ever called — see Engine's "Lifecycle
// contract" doc for the exact New/Run/Stop/Close state matrix. Engine.Import
// lets a host create or update a pipeline from a PipelineConfig value.
//
// PipelineConfig values don't have to come from YAML: NewPipeline starts a
// fluent, pipelines-in-code builder (PipelineBuilder, with
// ConnectorBuilder/ProcessorBuilder/DLQBuilder for nested entities) that
// produces the exact same PipelineConfig a YAML parse of the equivalent
// document produces — see builder.go and this package's round-trip tests
// (TestPipelineBuilder_RoundTrip, TestPipelineBuilder_RoundTrip_Fixtures) for
// the property this is verified against. Engine.ImportPipeline builds and
// imports in one call, so the common case never needs a separate
// provisioning/config import:
//
//	err = e.ImportPipeline(ctx, conduit.NewPipeline("hello").
//		WithConnector(conduit.NewSourceConnector("src", "builtin:generator")).
//		WithConnector(conduit.NewDestinationConnector("dst", "builtin:log")))
//
// The C-ABI/language-binding surface (libconduit proper, "B3") remains a
// later workstream; see docs/design-documents/20260722-embed-libconduit-v1.md.
//
// # Why this exists, not pkg/conduit directly
//
// pkg/conduit (docs/package_structure.md) is documented internal: its
// NewRuntime/Entrypoint.Serve API prints a splash to os.Stdout, calls os.Exit
// on error, writes logs unconditionally to os.Stdout, and registers metrics
// into the process-global Prometheus DefaultRegisterer — all correct defaults
// for a CLI process, all wrong for a library an application embeds alongside
// its own logging and metrics. This package wraps pkg/conduit with a handful
// of additive seams (see pkg/conduit.RuntimeOption) so the CLI's behavior is
// byte-for-byte unchanged while an embedder gets full control: every log line
// goes through a host-supplied *slog.Logger (or slog.Default(), an explicit,
// documented fallback — never os.Stdout), every metric goes through a
// host-supplied prometheus.Registerer (nil disables metrics entirely — never
// the default registry), and every failure is returned as an error, never an
// os.Exit or a panic.
//
// # Invariants
//
//   - Invariant 7 (graceful shutdown): Handle.Stop reuses Runtime.Run's
//     existing ctx-cancellation-driven drain (registerCleanup, dispatching to
//     registerCleanupV1/V2 — unchanged by this package) unchanged. Stop
//     supplies a different *trigger* (an explicit call cancelling Run's
//     context) for the identical mechanism `conduit run`'s SIGTERM handling
//     already exercises via Entrypoint.CancelOnInterrupt — it does not
//     reimplement or duplicate drain logic.
//   - Invariants 1-6 (ack/position/ordering/checkpoint/schema): not implicated.
//     This package does not touch the record data path. Engine.Import
//     delegates to provisioning.Service.Import unchanged; PipelineConfig is a
//     type alias (not a copy) of the exact struct the YAML provisioner
//     produces, so no new serialized shape is introduced.
//
// # Known limitation: metrics cross-talk across engines
//
// Two Engines in the same process, each with its own MetricsRegisterer, are
// genuinely isolated as distinct prometheus.Registerer/Registry *objects* —
// but pkg/foundation/metrics keeps process-global metric *definitions*, so a
// metric defined via that package's package-level constructors (everything in
// pkg/foundation/metrics/measure) still fans its *values* out to every
// registry ever registered in the process, including a second Engine's. This
// is a known, accepted limitation for v1 (not fixed by this package), tracked
// as a follow-up; see pkg/conduit.WithMetricsRegisterer's doc and
// TestTwoEngines_MetricsCrossTalk_KnownLimitation.
//
// Two sharper instances of the same root cause are tracked separately, also
// accepted for v1: a failed metrics registration during New still leaks a
// registry into pkg/foundation/metrics' process-global bookkeeping
// (https://github.com/ConduitIO/conduit/issues/2669, see
// pkg/conduit.configureEmbeddedMetrics' doc), and the HTTP /metrics route
// serves promclient.DefaultGatherer rather than a host-supplied
// MetricsRegisterer (https://github.com/ConduitIO/conduit/issues/2670, see
// pkg/conduit.(*Runtime).newHTTPMetricsHandler's doc).
//
// # Package boundary / deprecation policy
//
// This package's exported API (Options, Engine, Handle, PipelineConfig, New,
// NewPipeline/PipelineBuilder and its ConnectorBuilder/ProcessorBuilder/
// DLQBuilder companions) is a public contract, versioned like the connector
// protocol, pipeline config schema, and error codes: a breaking change is
// announced (CHANGELOG + a `Deprecated:` godoc comment) in one monthly
// release, kept working with a warning for at least one more minor release,
// and removed no earlier than the third minor release after announcement.
// PipelineConfig extends this policy by name to
// provisioning/config.{Pipeline,Connector,Processor,DLQ} —
// all four are constrained by the single doc comment above
// provisioning/config.Pipeline in that package's parser.go (Connector,
// Processor, and DLQ have no separate per-type comment of their own; they are
// covered by name in that one shared comment, not individually annotated).
package conduit
