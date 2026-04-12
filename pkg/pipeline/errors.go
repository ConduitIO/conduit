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

package pipeline

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"google.golang.org/grpc/codes"
)

var (
	ErrGracefulShutdown      = cerrors.New("graceful shutdown")
	ErrForceStop             = cerrors.New("force stop")
	ErrPipelineCannotRecover = cerrors.New("pipeline couldn't be recovered")

	// ErrPipelineRunning is returned when an operation can't be executed because the pipeline is running.
	ErrPipelineRunning = cerrors.NewWithGRPCCode(codes.FailedPrecondition, "pipeline is running")
	// ErrPipelineNotRunning is returned when an operation can't be executed because the pipeline is not running.
	ErrPipelineNotRunning = cerrors.NewWithGRPCCode(codes.FailedPrecondition, "pipeline not running")
	// ErrInstanceNotFound is returned when a pipeline with the given ID can't be found.
	ErrInstanceNotFound = cerrors.NewWithGRPCCode(codes.NotFound, "pipeline instance not found")
	// ErrNameMissing is returned when a pipeline name is expected but is empty.
	ErrNameMissing = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a pipeline name")
	// ErrIDMissing is returned when a pipeline ID is expected but is empty.
	ErrIDMissing = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a pipeline ID")
	// ErrNameAlreadyExists is returned when a pipeline with the given name already exists.
	ErrNameAlreadyExists = cerrors.NewWithGRPCCode(codes.AlreadyExists, "pipeline name already exists")
	// ErrInvalidCharacters is returned when a pipeline ID contains invalid characters.
	ErrInvalidCharacters = cerrors.NewWithGRPCCode(codes.InvalidArgument, "pipeline ID contains invalid characters")
	// ErrNameOverLimit is returned when a pipeline name is over the character limit.
	ErrNameOverLimit = cerrors.NewWithGRPCCode(codes.InvalidArgument, "pipeline name is over the character limit")
	// ErrIDOverLimit is returned when a pipeline ID is over the character limit.
	ErrIDOverLimit = cerrors.NewWithGRPCCode(codes.InvalidArgument, "pipeline ID is over the character limit")
	// ErrDescriptionOverLimit is returned when a pipeline description is over the character limit.
	ErrDescriptionOverLimit = cerrors.NewWithGRPCCode(codes.InvalidArgument, "pipeline description is over the character limit")
	// ErrConnectorIDNotFound is returned when a connector with the given ID can't be found in the pipeline.
	ErrConnectorIDNotFound = cerrors.NewWithGRPCCode(codes.NotFound, "connector ID not found in pipeline")
	// ErrProcessorIDNotFound is returned when a processor with the given ID can't be found in the pipeline.
	ErrProcessorIDNotFound = cerrors.NewWithGRPCCode(codes.NotFound, "processor ID not found in pipeline")
	// ErrMissingSourceConnectors is returned when a pipeline cannot be started due to missing source connectors.
	ErrMissingSourceConnectors = cerrors.NewWithGRPCCode(codes.InvalidArgument, "can't build pipeline without any source connectors")
)
