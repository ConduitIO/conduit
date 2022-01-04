package status

import (
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func PipelineError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, pipeline.ErrNameMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = CodeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func ConnectorError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, connector.ErrInvalidConnectorType):
		code = codes.InvalidArgument
	case cerrors.Is(err, connector.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = CodeFromError(err)
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
		code = CodeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func CodeFromError(err error) codes.Code {
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
	case cerrors.Is(err, orchestrator.ErrPipelineHasConnectorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrPipelineHasProcessorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrConnectorHasProcessorsAttached):
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}
