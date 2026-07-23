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

package conduit_test

import (
	"strings"
	"testing"

	conduit "github.com/conduitio/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

// TestPipelineBuilder_Build_MinimalPipeline proves the simplest possible
// builder chain produces the exact, unenriched PipelineConfig this package's
// doc promises: only ID is set, every other field left at its zero value.
func TestPipelineBuilder_Build_MinimalPipeline(t *testing.T) {
	is := is.New(t)

	got, err := conduit.NewPipeline("p1").Build()
	is.NoErr(err)

	want := conduit.PipelineConfig{ID: "p1"}
	is.True(cmp.Equal(got, want))
}

// TestPipelineBuilder_Build_FullPipeline exercises every field in one shot:
// connectors (with settings and a nested processor each), pipeline-scoped
// processors, and a DLQ — proving the builder produces the same shape a hand
// written config.Pipeline literal would.
func TestPipelineBuilder_Build_FullPipeline(t *testing.T) {
	is := is.New(t)

	got, err := conduit.NewPipeline("full").
		WithName("full-pipeline").
		WithDescription("exercises every builder field").
		WithStatus(conduit.StatusStopped).
		WithConnector(
			conduit.NewSourceConnector("src", "builtin:generator").
				WithName("generator-src").
				WithSetting("format.type", "raw").
				WithSettings(map[string]string{"recordCount": "5"}).
				WithProcessor(conduit.NewProcessor("srcproc", "js").WithCondition("true")),
		).
		WithConnector(
			conduit.NewDestinationConnector("dst", "builtin:log").WithName("log-dst"),
		).
		WithProcessor(
			conduit.NewProcessor("pipelineproc", "js").WithWorkers(2).WithSetting("k", "v"),
		).
		WithDLQ(
			conduit.NewDLQ("builtin:log").WithSetting("foo", "bar").WithWindowSize(4).WithWindowNackThreshold(2),
		).
		Build()
	is.NoErr(err)

	windowSize, windowNackThreshold := 4, 2
	want := conduit.PipelineConfig{
		ID:          "full",
		Name:        "full-pipeline",
		Description: "exercises every builder field",
		Status:      conduit.StatusStopped,
		Connectors: []provisioningconfig.Connector{
			{
				ID:     "src",
				Type:   conduit.ConnectorTypeSource,
				Plugin: "builtin:generator",
				Name:   "generator-src",
				Settings: map[string]string{
					"format.type": "raw",
					"recordCount": "5",
				},
				Processors: []provisioningconfig.Processor{
					{ID: "srcproc", Plugin: "js", Condition: "true"},
				},
			},
			{
				ID:     "dst",
				Type:   conduit.ConnectorTypeDestination,
				Plugin: "builtin:log",
				Name:   "log-dst",
			},
		},
		Processors: []provisioningconfig.Processor{
			{ID: "pipelineproc", Plugin: "js", Workers: 2, Settings: map[string]string{"k": "v"}},
		},
		DLQ: provisioningconfig.DLQ{
			Plugin:              "builtin:log",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          &windowSize,
			WindowNackThreshold: &windowNackThreshold,
		},
	}
	is.True(cmp.Equal(got, want))
}

// TestPipelineBuilder_Build_RejectsMissingID proves Build reuses
// provisioning/config.Validate — a required-field violation surfaces as a
// coded error, not a panic, and not a later Import-time failure.
func TestPipelineBuilder_Build_RejectsMissingID(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("").Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, provisioningconfig.CodeFieldRequired)
}

// TestPipelineBuilder_Build_RejectsMissingConnectorPlugin proves a connector
// missing its required plugin is caught at Build time.
func TestPipelineBuilder_Build_RejectsMissingConnectorPlugin(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithConnector(conduit.NewSourceConnector("src", "")).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, provisioningconfig.CodeFieldRequired)
}

// TestPipelineBuilder_Build_RejectsDuplicateConnectorIDs proves the edge
// case flagged by the embed workstream plan (§7): a fluent builder makes an
// accidental duplicate ID easy to write, and Build must catch it — the same
// way `conduit pipelines validate` catches a YAML pipeline with two
// same-ID connectors — rather than surfacing a confusing failure later at
// Import/provisioning time.
func TestPipelineBuilder_Build_RejectsDuplicateConnectorIDs(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithConnector(conduit.NewSourceConnector("dup", "builtin:generator")).
		WithConnector(conduit.NewDestinationConnector("dup", "builtin:log")).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, provisioningconfig.CodeIDDuplicate)
}

// TestPipelineBuilder_Build_RejectsDuplicateProcessorIDs mirrors the
// connector case for pipeline-scoped processors.
func TestPipelineBuilder_Build_RejectsDuplicateProcessorIDs(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithProcessor(conduit.NewProcessor("dup", "js")).
		WithProcessor(conduit.NewProcessor("dup", "js")).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, provisioningconfig.CodeIDDuplicate)
}

// TestDLQBuilder_WindowFields_NilVsExplicitZero proves the edge case the
// embed workstream plan calls out by name (§7): DLQ.WindowSize/
// WindowNackThreshold are *int specifically so "never called" (nil) and
// "explicitly set to zero" (non-nil, pointing at 0) stay distinguishable —
// the builder must never silently default an unset field to a *0.
func TestDLQBuilder_WindowFields_NilVsExplicitZero(t *testing.T) {
	is := is.New(t)

	t.Run("unset stays nil", func(t *testing.T) {
		got, err := conduit.NewPipeline("p1").
			WithDLQ(conduit.NewDLQ("builtin:log")).
			Build()
		is.NoErr(err)
		is.True(got.DLQ.WindowSize == nil)
		is.True(got.DLQ.WindowNackThreshold == nil)
	})

	t.Run("explicit zero is a non-nil pointer to 0", func(t *testing.T) {
		got, err := conduit.NewPipeline("p1").
			WithDLQ(conduit.NewDLQ("builtin:log").WithWindowSize(0).WithWindowNackThreshold(0)).
			Build()
		is.NoErr(err)
		is.True(got.DLQ.WindowSize != nil)
		is.Equal(*got.DLQ.WindowSize, 0)
		is.True(got.DLQ.WindowNackThreshold != nil)
		is.Equal(*got.DLQ.WindowNackThreshold, 0)
	})
}

// TestPipelineBuilder_WithDLQ_Omitted_LeavesZeroValue proves that never
// calling WithDLQ leaves PipelineConfig.DLQ at its zero value — matching a
// YAML pipeline with no dead-letter-queue block — rather than the builder
// pre-emptively applying Conduit's default DLQ (that is Engine.Import's
// enrichment's job, not Build's, see Build's doc).
func TestPipelineBuilder_WithDLQ_Omitted_LeavesZeroValue(t *testing.T) {
	is := is.New(t)

	got, err := conduit.NewPipeline("p1").Build()
	is.NoErr(err)
	is.True(cmp.Equal(got.DLQ, provisioningconfig.DLQ{}))
}

// TestConnectorBuilder_WithSettings_MergesWithoutClobbering proves
// WithSettings merges rather than replaces previously set keys.
func TestConnectorBuilder_WithSettings_MergesWithoutClobbering(t *testing.T) {
	is := is.New(t)

	got, err := conduit.NewPipeline("p1").
		WithConnector(
			conduit.NewSourceConnector("src", "builtin:generator").
				WithSetting("a", "1").
				WithSettings(map[string]string{"b": "2", "a": "override"}),
		).
		Build()
	is.NoErr(err)

	is.Equal(got.Connectors[0].Settings, map[string]string{"a": "override", "b": "2"})
}

// TestPipelineBuilder_NestedBuilderReuse_SnapshotsAtAttachTime proves the
// documented contract on PipelineBuilder: With... snapshots a nested
// builder's state at the moment it's attached, so mutating a
// *ConnectorBuilder after it has already been attached does not
// retroactively change the pipeline.
func TestPipelineBuilder_NestedBuilderReuse_SnapshotsAtAttachTime(t *testing.T) {
	is := is.New(t)

	src := conduit.NewSourceConnector("src", "builtin:generator")
	got, err := conduit.NewPipeline("p1").WithConnector(src).Build()
	is.NoErr(err)

	// Mutating src after it was attached must not affect the already-built
	// pipeline.
	src.WithName("mutated-after-attach")

	is.Equal(got.Connectors[0].Name, "")
}

// TestPipelineBuilder_NestedBuilderReuse_AcrossPipelines proves the second
// half of the documented snapshot-at-attach contract: reusing a
// *ConnectorBuilder across two separate pipelines (mutating it between
// attach calls) produces two independent PipelineConfig values, each
// holding only the state as of its own attach call — never a later
// mutation, and never a mutation made "for" a different pipeline.
func TestPipelineBuilder_NestedBuilderReuse_AcrossPipelines(t *testing.T) {
	is := is.New(t)

	src := conduit.NewSourceConnector("src", "builtin:generator")

	p1, err := conduit.NewPipeline("p1").WithConnector(src).Build()
	is.NoErr(err)
	is.Equal(p1.Connectors[0].Name, "")

	src.WithName("renamed")

	p2, err := conduit.NewPipeline("p2").WithConnector(src).Build()
	is.NoErr(err)
	is.Equal(p2.Connectors[0].Name, "renamed")

	// p1 must remain exactly as it was built, unaffected by the mutation
	// and by p2's later, independent snapshot.
	is.Equal(p1.Connectors[0].Name, "")
}

// TestPipelineBuilder_Build_NilConnector proves the DX fix: WithConnector(nil)
// never panics. It is recorded as a conduiterr.CodeInvalidArgument error and
// surfaced from the next Build call, exactly like a validation failure —
// no new error code is minted (see Run/Stop misuse in conduit.go for the
// precedent this follows).
func TestPipelineBuilder_Build_NilConnector(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithConnector(nil).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestPipelineBuilder_Build_NilProcessor mirrors the nil-connector case for
// a pipeline-scoped processor.
func TestPipelineBuilder_Build_NilProcessor(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithProcessor(nil).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestPipelineBuilder_Build_NilDLQ mirrors the nil-connector case for
// WithDLQ.
func TestPipelineBuilder_Build_NilDLQ(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithDLQ(nil).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestConnectorBuilder_WithProcessor_NilProcessor proves the nested case: a
// nil processor passed to a *ConnectorBuilder's own WithProcessor is
// recorded there and still surfaces from the top-level PipelineBuilder's
// Build call once the connector is attached — the error is not lost when
// crossing from ConnectorBuilder into PipelineBuilder.
func TestConnectorBuilder_WithProcessor_NilProcessor(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithConnector(
			conduit.NewSourceConnector("src", "builtin:generator").WithProcessor(nil),
		).
		Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestPipelineBuilder_Build_NilPipelineBuilder proves Build on a nil
// *PipelineBuilder is a coded error, not a nil-pointer panic — the same
// defensive pattern B1 already applies to Handle.Stop on a nil *Handle.
func TestPipelineBuilder_Build_NilPipelineBuilder(t *testing.T) {
	is := is.New(t)

	var b *conduit.PipelineBuilder
	_, err := b.Build()
	is.True(err != nil)

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
}

// TestPipelineBuilder_Build_MultipleNilArgs_JoinsErrors proves multiple
// nil-argument misuses in one chain are all surfaced from a single Build
// call (joined via cerrors.Join, the same as Validate's own multi-error
// pipeline-config failures), not just the first one.
func TestPipelineBuilder_Build_MultipleNilArgs_JoinsErrors(t *testing.T) {
	is := is.New(t)

	_, err := conduit.NewPipeline("p1").
		WithConnector(nil).
		WithProcessor(nil).
		WithDLQ(nil).
		Build()
	is.True(err != nil)

	msg := err.Error()
	for _, want := range []string{"WithConnector", "WithProcessor", "WithDLQ"} {
		is.True(strings.Contains(msg, want))
	}
}
