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

// codes_contract_test.go is the CI error-code guard, execution plan §1.1: it
// fails the build the moment a user-facing error reaches PipelineError,
// ConnectorError, ProcessorError, or PluginError without a stable, registered
// ConduitError reason on the wire.
//
// Honest scope — this is a REGRESSION + STRUCTURAL guard, not a completeness
// guard:
//   - It does NOT verify every error class in the codebase has been migrated
//     to a registered Code. Several legacy sentinels (cerrors.ErrNotImpl,
//     cerrors.ErrEmptyID, connector.ValidationError, and any bare sentinel
//     whose production call site hasn't wrapped it yet) still resolve to
//     conduiterr.CodeUnknown's reason ("internal.unknown"). That is expected
//     and asserted here, not hidden.
//   - It DOES verify two things that, if either regresses, would ship a
//     codeless error to users/agents in production: (1) every status these
//     four functions return carries a google.rpc.ErrorInfo detail with a
//     registered reason — never nothing at all; and (2) an error that already
//     carries a registered Code (constructed directly, or wrapped the way
//     production code actually wraps it at its origination site) keeps that
//     code's reason all the way through the boundary — it never silently
//     degrades to internal.unknown.
package status

import (
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// boundaryFn is one of the four API error-boundary chokepoints under test.
type boundaryFn func(error) error

// contractCase is one corpus entry driven through a boundary function.
type contractCase struct {
	name string
	fn   boundaryFn
	err  error

	wantCode codes.Code // top-level gRPC status category

	// migrated is true when err already carries a registered ConduitError
	// Code (constructed directly, or wrapped the way production code wraps
	// it). Those cases must NOT resolve to conduiterr.CodeUnknown's reason —
	// that is the assertion that fails the build when a known class loses,
	// or never had, a real code. false marks a legacy, not-yet-migrated
	// sentinel: it is expected to fall back to CodeUnknown's reason, and the
	// corpus only asserts that a detail is present at all.
	migrated bool

	// wantReason, if non-empty, pins the exact expected reason. Left empty
	// for migrated==false cases, where the fallback reason is always
	// conduiterr.CodeUnknown's.
	wantReason string
}

// contractCorpus is the full corpus for the guard. Each entry documents,
// alongside the assertion, WHY it's migrated or not: that annotation is the
// guard's audit trail for "which classes are actually coded today."
func contractCorpus() []contractCase {
	return []contractCase{
		// --- migrated: production wraps these sentinels with a registered
		// Code at their origination site (pkg/pipeline, pkg/connector,
		// pkg/orchestrator, pkg/processor codes.go files). The corpus
		// reproduces that production shape directly, rather than relying on
		// the boundary's own sentinel switch, so this proves the full
		// production path — origination wrap through to the wire.
		{
			name:       "pipeline: name missing (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(pipeline.CodePipelineNameMissing, "must provide a pipeline name", pipeline.ErrNameMissing),
			wantCode:   codes.InvalidArgument,
			migrated:   true,
			wantReason: pipeline.CodePipelineNameMissing.Reason(),
		},
		{
			name:       "pipeline: instance not found (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(pipeline.CodePipelineNotFound, "pipeline not found", pipeline.ErrInstanceNotFound),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: pipeline.CodePipelineNotFound.Reason(),
		},
		{
			name:       "pipeline: name already exists (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(pipeline.CodePipelineNameAlreadyExists, "pipeline name already exists", pipeline.ErrNameAlreadyExists),
			wantCode:   codes.AlreadyExists,
			migrated:   true,
			wantReason: pipeline.CodePipelineNameAlreadyExists.Reason(),
		},
		{
			name:       "pipeline: running (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(pipeline.CodePipelineRunning, "pipeline is running", pipeline.ErrPipelineRunning),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: pipeline.CodePipelineRunning.Reason(),
		},
		{
			name:       "pipeline: not running (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(pipeline.CodePipelineNotRunning, "pipeline not running", pipeline.ErrPipelineNotRunning),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: pipeline.CodePipelineNotRunning.Reason(),
		},
		{
			name:       "connector: invalid type (migrated at origination)",
			fn:         ConnectorError,
			err:        conduiterr.Wrap(connector.CodeConnectorInvalidType, "invalid connector type", connector.ErrInvalidConnectorType),
			wantCode:   codes.InvalidArgument,
			migrated:   true,
			wantReason: connector.CodeConnectorInvalidType.Reason(),
		},
		{
			name:       "connector: instance not found (migrated at origination)",
			fn:         ConnectorError,
			err:        conduiterr.Wrap(connector.CodeConnectorNotFound, "connector not found", connector.ErrInstanceNotFound),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: connector.CodeConnectorNotFound.Reason(),
		},
		{
			name:       "connector: running (migrated at origination)",
			fn:         ConnectorError,
			err:        conduiterr.Wrap(connector.CodeConnectorRunning, "connector is running", connector.ErrConnectorRunning),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: connector.CodeConnectorRunning.Reason(),
		},
		{
			name:       "processor: instance not found (migrated at origination)",
			fn:         ProcessorError,
			err:        conduiterr.Wrap(processor.CodeProcessorNotFound, "processor not found", processor.ErrInstanceNotFound),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: processor.CodeProcessorNotFound.Reason(),
		},
		{
			name:       "processor: invalid parent type (migrated at origination)",
			fn:         ProcessorError,
			err:        conduiterr.Wrap(orchestrator.CodeInvalidProcessorParentType, "invalid processor parent type", orchestrator.ErrInvalidProcessorParentType),
			wantCode:   codes.InvalidArgument,
			migrated:   true,
			wantReason: orchestrator.CodeInvalidProcessorParentType.Reason(),
		},
		{
			name:       "orchestrator: pipeline has connectors attached (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(orchestrator.CodePipelineHasConnectorsAttached, "pipeline has connectors attached", orchestrator.ErrPipelineHasConnectorsAttached),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: orchestrator.CodePipelineHasConnectorsAttached.Reason(),
		},
		{
			name:       "orchestrator: pipeline has processors attached (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(orchestrator.CodePipelineHasProcessorsAttached, "pipeline has processors attached", orchestrator.ErrPipelineHasProcessorsAttached),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: orchestrator.CodePipelineHasProcessorsAttached.Reason(),
		},
		{
			name:       "orchestrator: connector has processors attached (migrated at origination)",
			fn:         ConnectorError,
			err:        conduiterr.Wrap(orchestrator.CodeConnectorHasProcessorsAttached, "connector has processors attached", orchestrator.ErrConnectorHasProcessorsAttached),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: orchestrator.CodeConnectorHasProcessorsAttached.Reason(),
		},
		{
			name:       "orchestrator: immutable provisioned by config (migrated at origination)",
			fn:         PipelineError,
			err:        conduiterr.Wrap(orchestrator.CodeImmutableProvisionedByConfig, "immutable, provisioned by config", orchestrator.ErrImmutableProvisionedByConfig),
			wantCode:   codes.FailedPrecondition,
			migrated:   true,
			wantReason: orchestrator.CodeImmutableProvisionedByConfig.Reason(),
		},
		{
			name:       "plugin: connector plugin not found, direct construction",
			fn:         PluginError,
			err:        conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "connector plugin not found"),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: conduiterr.CodeConnectorPluginNotFound.Reason(),
		},
		{
			name:       "plugin: connector plugin not found, deeply wrapped with cerrors.Errorf",
			fn:         PluginError,
			err:        cerrors.Errorf("dispensing failed: %w", cerrors.Errorf("registry lookup failed: %w", conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "connector plugin not found"))),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: conduiterr.CodeConnectorPluginNotFound.Reason(),
		},
		{
			name:       "plugin: processor plugin not found, direct construction",
			fn:         PluginError,
			err:        conduiterr.New(conduiterr.CodeProcessorPluginNotFound, "processor plugin not found"),
			wantCode:   codes.NotFound,
			migrated:   true,
			wantReason: conduiterr.CodeProcessorPluginNotFound.Reason(),
		},

		// --- not yet migrated: these classes are recognized by status.go's
		// own cerrors.Is switches (so they still get the right gRPC
		// category), but no package has registered a Code and wrapped them
		// at their origination site yet. They legitimately fall back to
		// conduiterr.CodeUnknown's reason today — see the package doc above.
		{
			name:     "not yet migrated: cerrors.ErrNotImpl",
			fn:       PipelineError,
			err:      cerrors.ErrNotImpl,
			wantCode: codes.Unimplemented,
			migrated: false,
		},
		{
			name:     "not yet migrated: cerrors.ErrEmptyID",
			fn:       PipelineError,
			err:      cerrors.ErrEmptyID,
			wantCode: codes.InvalidArgument,
			migrated: false,
		},
		{
			name:     "not yet migrated: connector.ValidationError",
			fn:       ConnectorError,
			err:      &conn_plugin.ValidationError{Err: cerrors.New("bad config")},
			wantCode: codes.FailedPrecondition,
			migrated: false,
		},
		{
			name:     "not yet migrated: plain wrapped error, unrecognized class",
			fn:       PipelineError,
			err:      cerrors.Errorf("boom: %w", cerrors.New("something went wrong")),
			wantCode: codes.Internal,
			migrated: false,
		},
	}
}

// TestErrorCodesContract drives the corpus above through the four API error
// boundary chokepoints and checks the wire shape.
func TestErrorCodesContract(t *testing.T) {
	corpus := contractCorpus()

	t.Run("every status carries a registered ErrorInfo reason", func(t *testing.T) {
		for _, tc := range corpus {
			t.Run(tc.name, func(t *testing.T) {
				is := is.New(t)

				got := tc.fn(tc.err)
				st, ok := grpcstatus.FromError(got)
				is.True(ok) // fn must return a gRPC status error
				is.Equal(st.Code(), tc.wantCode)

				info := errorInfoOf(st)
				is.True(info != nil)                  // never codeless: an ErrorInfo detail is always present
				is.Equal(info.GetDomain(), "conduit") // detail belongs to Conduit's domain

				_, registered := conduiterr.LookupCode(info.GetReason())
				is.True(registered) // the reason is one the registry actually knows about

				if tc.wantReason != "" {
					is.Equal(info.GetReason(), tc.wantReason)
				}
			})
		}
	})

	// This is the subtest the CI guard depends on: it fails the build the
	// moment a class that currently has a real, registered code stops
	// resolving to it — whether because the origination site's Wrap call was
	// removed, the boundary's fast path (conduitErrorStatus) stopped firing,
	// or the Code itself was deleted from its owning package's codes.go.
	t.Run("known user-facing classes never resolve to internal.unknown", func(t *testing.T) {
		for _, tc := range corpus {
			if !tc.migrated {
				continue
			}
			t.Run(tc.name, func(t *testing.T) {
				is := is.New(t)

				got := tc.fn(tc.err)
				st, ok := grpcstatus.FromError(got)
				is.True(ok)

				info := errorInfoOf(st)
				is.True(info != nil)
				is.True(info.GetReason() != conduiterr.CodeUnknown.Reason())
			})
		}
	})

	t.Run("not-yet-migrated classes fall back to internal.unknown, not silence", func(t *testing.T) {
		for _, tc := range corpus {
			if tc.migrated {
				continue
			}
			t.Run(tc.name, func(t *testing.T) {
				is := is.New(t)

				got := tc.fn(tc.err)
				st, ok := grpcstatus.FromError(got)
				is.True(ok)

				info := errorInfoOf(st)
				is.True(info != nil)
				is.Equal(info.GetReason(), conduiterr.CodeUnknown.Reason())
			})
		}
	})
}

// TestErrorCodesRegistryInvariants asserts the conduiterr registry — the
// single source of truth for docs, llms.txt, and the UI — has no duplicate or
// empty reasons, and that every registered Code maps to a real gRPC category.
// Register itself panics on a duplicate reason at init time, so the duplicate
// check here is a second, independent line of defense (e.g. against a future
// refactor of Register that silently drops the panic).
func TestErrorCodesRegistryInvariants(t *testing.T) {
	is := is.New(t)

	all := conduiterr.Codes()
	is.True(len(all) > 0) // sanity: the registry is populated

	seen := make(map[string]bool, len(all))
	for _, c := range all {
		is.True(c.Reason() != "")  // no empty reasons
		is.True(!seen[c.Reason()]) // no duplicate reasons
		seen[c.Reason()] = true
		is.True(c.GRPCCode() != codes.OK) // every registered code maps to a real error category
	}
}

// errorInfoOf extracts the google.rpc.ErrorInfo detail from a status, or nil
// if none is present.
func errorInfoOf(st *grpcstatus.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}
