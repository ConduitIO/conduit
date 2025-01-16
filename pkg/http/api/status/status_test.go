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
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestPipelineError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want error
	}{
		{
			name: "pipeline name error returns invalid argument grpc error",
			args: args{
				err: pipeline.ErrNameMissing,
			},
			want: grpcstatus.Error(codes.InvalidArgument, pipeline.ErrNameMissing.Error()),
		},
		{
			name: "pipeline not found error returns not found grpc error",
			args: args{
				err: pipeline.ErrInstanceNotFound,
			},
			want: grpcstatus.Error(codes.NotFound, pipeline.ErrInstanceNotFound.Error()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			is.Equal(tt.want, PipelineError(tt.args.err))
		})
	}
}

func TestConnectorError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want error
	}{
		{
			name: "invalid connector type returns invalid argument grpc error",
			args: args{
				err: connector.ErrInvalidConnectorType,
			},
			want: grpcstatus.Error(codes.InvalidArgument, connector.ErrInvalidConnectorType.Error()),
		},
		{
			name: "connector not found error returns not found grpc error",
			args: args{
				err: connector.ErrInstanceNotFound,
			},
			want: grpcstatus.Error(codes.NotFound, connector.ErrInstanceNotFound.Error()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			is.Equal(tt.want, ConnectorError(tt.args.err))
		})
	}
}

func TestProcessorError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want error
	}{
		{
			name: "invalid processor parent type returns invalid argument grpc error",
			args: args{
				err: orchestrator.ErrInvalidProcessorParentType,
			},
			want: grpcstatus.Error(codes.InvalidArgument, orchestrator.ErrInvalidProcessorParentType.Error()),
		},
		{
			name: "processor not found error returns not found grpc error",
			args: args{
				err: processor.ErrInstanceNotFound,
			},
			want: grpcstatus.Error(codes.NotFound, processor.ErrInstanceNotFound.Error()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			is.Equal(tt.want, ProcessorError(tt.args.err))
		})
	}
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
