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
	case cerrors.Is(err, pipeline.ErrNameMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrIDMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrNameAlreadyExists):
		code = codes.AlreadyExists
	case cerrors.Is(err, pipeline.ErrInvalidCharacters):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrNameOverLimit):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrIDOverLimit):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrDescriptionOverLimit):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrInstanceNotFound):
		code = codes.NotFound
	case cerrors.Is(err, pipeline.ErrConnectorIDNotFound):
		code = codes.NotFound
	case cerrors.Is(err, pipeline.ErrProcessorIDNotFound):
		code = codes.NotFound
	case cerrors.Is(err, pipeline.ErrInvalidPipelineStructure):
		code = codes.FailedPrecondition
	case cerrors.Is(err, pipeline.ErrInvalidDLQConfig):
		code = codes.InvalidArgument
	default:
		code = codeFromError(err)
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
	case cerrors.Is(err, connector.ErrNameMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, connector.ErrIDMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, connector.ErrNameAlreadyExists):
		code = codes.AlreadyExists
	case cerrors.Is(err, connector.ErrPipelineIDMissing):
		code = codes.InvalidArgument
	default:
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
	case cerrors.Is(err, processor.ErrNameMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, processor.ErrIDMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, processor.ErrNameAlreadyExists):
		code = codes.AlreadyExists
	case cerrors.Is(err, processor.ErrParentIDMissing):
		code = codes.InvalidArgument
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
	case cerrors.Is(err, &conn_plugin.ValidationError{}):
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
		return codes.Internal
	}
}
