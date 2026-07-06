// Copyright © 2022 Meroxa, Inc.
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

package status

import (
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// reasonOf extracts the google.rpc.ErrorInfo reason from a gRPC status error,
// or "" if none is present. Tests use this instead of comparing errors
// directly now that every boundary function attaches a detail (see D1, the
// CodeUnknown fallback wiring): two grpcstatus.Error values built the same way
// compare equal, but a status with a details slice does not compare equal via
// is.Equal, so assertions here check the pieces (code, message, reason)
// instead of the whole error value.
func reasonOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := grpcstatus.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

func TestPipelineError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name     string
		args     args
		wantMsg  string
		wantCode codes.Code
	}{
		{
			name: "pipeline name error returns invalid argument grpc error",
			args: args{
				err: pipeline.ErrNameMissing,
			},
			wantMsg:  pipeline.ErrNameMissing.Error(),
			wantCode: codes.InvalidArgument,
		},
		{
			name: "pipeline not found error returns not found grpc error",
			args: args{
				err: pipeline.ErrInstanceNotFound,
			},
			wantMsg:  pipeline.ErrInstanceNotFound.Error(),
			wantCode: codes.NotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			got := PipelineError(tt.args.err)
			st, ok := grpcstatus.FromError(got)
			is.True(ok)
			is.Equal(st.Code(), tt.wantCode)
			is.Equal(st.Message(), tt.wantMsg)
			// D1: the fallback always attaches an ErrorInfo detail, even though
			// these bare sentinels have not been migrated to a registered Code yet.
			is.Equal(reasonOf(t, got), conduiterr.CodeUnknown.Reason())
		})
	}
}

func TestConnectorError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name     string
		args     args
		wantMsg  string
		wantCode codes.Code
	}{
		{
			name: "invalid connector type returns invalid argument grpc error",
			args: args{
				err: connector.ErrInvalidConnectorType,
			},
			wantMsg:  connector.ErrInvalidConnectorType.Error(),
			wantCode: codes.InvalidArgument,
		},
		{
			name: "connector not found error returns not found grpc error",
			args: args{
				err: connector.ErrInstanceNotFound,
			},
			wantMsg:  connector.ErrInstanceNotFound.Error(),
			wantCode: codes.NotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			got := ConnectorError(tt.args.err)
			st, ok := grpcstatus.FromError(got)
			is.True(ok)
			is.Equal(st.Code(), tt.wantCode)
			is.Equal(st.Message(), tt.wantMsg)
			is.Equal(reasonOf(t, got), conduiterr.CodeUnknown.Reason())
		})
	}
}

func TestProcessorError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name     string
		args     args
		wantMsg  string
		wantCode codes.Code
	}{
		{
			name: "invalid processor parent type returns invalid argument grpc error",
			args: args{
				err: orchestrator.ErrInvalidProcessorParentType,
			},
			wantMsg:  orchestrator.ErrInvalidProcessorParentType.Error(),
			wantCode: codes.InvalidArgument,
		},
		{
			name: "processor not found error returns not found grpc error",
			args: args{
				err: processor.ErrInstanceNotFound,
			},
			wantMsg:  processor.ErrInstanceNotFound.Error(),
			wantCode: codes.NotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			got := ProcessorError(tt.args.err)
			st, ok := grpcstatus.FromError(got)
			is.True(ok)
			is.Equal(st.Code(), tt.wantCode)
			is.Equal(st.Message(), tt.wantMsg)
			is.Equal(reasonOf(t, got), conduiterr.CodeUnknown.Reason())
		})
	}
}

// TestConduitErrorFlowsThroughBoundary proves the ConduitError foundation reaches
// the API: a ConduitError (even wrapped) yields a gRPC status with the code's
// category and an additive google.rpc.ErrorInfo detail carrying the stable reason,
// configPath, and suggestion.
func TestConduitErrorFlowsThroughBoundary(t *testing.T) {
	is := is.New(t)

	ce := conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "connector plugin not found")
	ce.ConfigPath = "/connectors/0/plugin"
	ce.Suggestion = "run `conduit connectors install <name>`"

	// wrapped, to prove errors.As finds it through the chain
	got := PipelineError(cerrors.Errorf("provisioning failed: %w", ce))

	st, ok := grpcstatus.FromError(got)
	is.True(ok)
	is.Equal(st.Code(), codes.NotFound) // the code's gRPC category

	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			info = ei
		}
	}
	is.True(info != nil) // the structured detail is present
	is.Equal(info.GetReason(), conduiterr.CodeConnectorPluginNotFound.Reason())
	is.Equal(info.GetDomain(), "conduit")
	is.Equal(info.GetMetadata()["configPath"], "/connectors/0/plugin")
	is.Equal(info.GetMetadata()["suggestion"], "run `conduit connectors install <name>`")
}

// TestNonConduitErrorUnchanged confirms the wiring is additive: a plain sentinel
// error still maps to the same category and message as before ConduitError
// existed. It now also carries an ErrorInfo detail (the D1 fallback wiring),
// which is the one deliberate, backward-compatible change: previously this
// path returned a codeless status.
func TestNonConduitErrorUnchanged(t *testing.T) {
	is := is.New(t)
	got := PipelineError(pipeline.ErrInstanceNotFound)

	st, ok := grpcstatus.FromError(got)
	is.True(ok)
	is.Equal(st.Code(), codes.NotFound)
	is.Equal(st.Message(), pipeline.ErrInstanceNotFound.Error())
	is.Equal(reasonOf(t, got), conduiterr.CodeUnknown.Reason())
}

func TestCodeFromError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want codes.Code
	}{
		{
			name: "unimplemented error returns unimplemented grpc code",
			args: args{
				err: cerrors.ErrNotImpl,
			},
			want: codes.Unimplemented,
		},
		{
			name: "empty id error returns invalid argument grpc error",
			args: args{
				err: cerrors.ErrEmptyID,
			},
			want: codes.InvalidArgument,
		},
		{
			name: "pipeline running error returns failed precondition grpc error",
			args: args{
				err: pipeline.ErrPipelineRunning,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "pipeline not running error returns failed precondition grpc error",
			args: args{
				err: pipeline.ErrPipelineNotRunning,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "pipeline name already exists error returns already exists grpc error",
			args: args{
				err: pipeline.ErrNameAlreadyExists,
			},
			want: codes.AlreadyExists,
		},
		{
			name: "connector running error returns failed precondition grpc error",
			args: args{
				err: connector.ErrConnectorRunning,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "pipeline has connectors attached error returns failed precondition grpc error",
			args: args{
				err: orchestrator.ErrPipelineHasConnectorsAttached,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "pipeline has processors attached error returns failed precondition grpc error",
			args: args{
				err: orchestrator.ErrPipelineHasProcessorsAttached,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "connector has processors attached error returns failed precondition grpc error",
			args: args{
				err: orchestrator.ErrConnectorHasProcessorsAttached,
			},
			want: codes.FailedPrecondition,
		},
		{
			name: "an unknown error returns internal grpc error",
			args: args{
				err: cerrors.New("I am an error"),
			},
			want: codes.Internal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			is.Equal(tt.want, codeFromError(tt.args.err))
		})
	}
}
