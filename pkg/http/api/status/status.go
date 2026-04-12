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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func PipelineError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, pipeline.ErrInstanceNotFound):
		code = codes.NotFound
	case cerrors.Is(err, pipeline.ErrNameMissing),
		cerrors.Is(err, pipeline.ErrIDMissing),
		cerrors.Is(err, pipeline.ErrInvalidCharacters),
		cerrors.Is(err, pipeline.ErrNameOverLimit),
		cerrors.Is(err, pipeline.ErrIDOverLimit),
		cerrors.Is(err, pipeline.ErrDescriptionOverLimit),
		cerrors.Is(err, pipeline.ErrDLQPluginMissing),
		cerrors.Is(err, pipeline.ErrDLQWindowSizeInvalid),
		cerrors.Is(err, pipeline.ErrDLQNackThresholdInvalid),
		cerrors.Is(err, pipeline.ErrDLQNackThresholdTooHigh):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrPipelineStartFailed):
		code = codes.FailedPrecondition
	default:
		// Fallback to generic error mapping if not a pipeline-specific error
		code = codeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func ConnectorError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, connector.ErrInstanceNotFound):
		code = codes.NotFound
	case cerrors.Is(err, connector.ErrInvalidConnectorType),
		cerrors.Is(err, connector.ErrNameMissing),
		cerrors.Is(err, connector.ErrIDMissing),
		cerrors.Is(err, connector.ErrInvalidCharacters),
		cerrors.Is(err, connector.ErrNameOverLimit),
		cerrors.Is(err, connector.ErrIDOverLimit),
		cerrors.Is(err, connector.ErrPluginMissing),
		cerrors.Is(err, connector.ErrPipelineIDMissing):
		code = codes.InvalidArgument
	default:
		// Fallback to generic error mapping
		code = codeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func ProcessorError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, orchestrator.ErrInvalidProcessorParentType):
		code = codes.InvalidArgument
	case cerrors.Is(err, processor.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = codeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func PluginError(err error) error {
	return grpcstatus.Error(codeFromError(err), err.Error())
}

func codeFromError(err error) codes.Code {
	switch {
	case cerrors.Is(err, cerrors.ErrNotImpl):
		return codes.Unimplemented
	case cerrors.Is(err, cerrors.ErrEmptyID):
		return codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrPipelineRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, pipeline.ErrPipelineNotRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, pipeline.ErrNameAlreadyExists):
		return codes.AlreadyExists
	case cerrors.Is(err, connector.ErrConnectorRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, &conn_plugin.ValidationError{}): // Catches "invalid plugin config" errors
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrPipelineHasConnectorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrPipelineHasProcessorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrConnectorHasProcessorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrImmutableProvisionedByConfig):
		return codes.FailedPrecondition
	default:
		// If none of the specific errors matched, it's an internal server error by default
		return codes.Internal
	}
}
