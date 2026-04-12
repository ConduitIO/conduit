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
	// Map all client-side pipeline configuration/validation errors to InvalidArgument
	case cerrors.Is(err, pipeline.ErrNameMissing),
		cerrors.Is(err, pipeline.ErrIDMissing),
		cerrors.Is(err, pipeline.ErrInvalidCharacters),
		cerrors.Is(err, pipeline.ErrNameOverLimit),
		cerrors.Is(err, pipeline.ErrIDOverLimit),
		cerrors.Is(err, pipeline.ErrDescriptionOverLimit),
		cerrors.Is(err, pipeline.ErrInvalidPipelineConfig):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrNameAlreadyExists):
		code = codes.AlreadyExists
	default:
		code = codeFromError(err)
	}

	return grpcstatus.Error(code, err.Error())
}

func ConnectorError(err error) error {
	var code codes.Code

	switch {
	case cerrors.Is(err, connector.ErrInstanceNotFound):
		code = codes.NotFound
	// Map all client-side connector configuration/validation errors to InvalidArgument
	case cerrors.Is(err, connector.ErrInvalidConnectorType),
		cerrors.Is(err, connector.ErrNameMissing),
		cerrors.Is(err, connector.ErrIDMissing),
		cerrors.Is(err, connector.ErrInvalidCharacters),
		cerrors.Is(err, connector.ErrNameOverLimit),
		cerrors.Is(err, connector.ErrIDOverLimit),
		cerrors.Is(err, connector.ErrPluginMissing),
		cerrors.Is(err, connector.ErrPipelineIDMissing),
		cerrors.Is(err, connector.ErrInvalidConnectorConfig):
		code = codes.InvalidArgument
	case cerrors.Is(err, connector.ErrNameAlreadyExists):
		code = codes.AlreadyExists
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
		return codes.InvalidArgument // Consider replacing with specific ErrIDMissing from respective packages
	case cerrors.Is(err, pipeline.ErrPipelineRunning),
		cerrors.Is(err, pipeline.ErrPipelineNotRunning),
		cerrors.Is(err, connector.ErrConnectorRunning),
		cerrors.Is(err, orchestrator.ErrPipelineHasConnectorsAttached),
		cerrors.Is(err, orchestrator.ErrPipelineHasProcessorsAttached),
		cerrors.Is(err, orchestrator.ErrConnectorHasProcessorsAttached),
		cerrors.Is(err, orchestrator.ErrImmutableProvisionedByConfig):
		return codes.FailedPrecondition
	// Map plugin validation errors and orchestrator invalid config to InvalidArgument
	case cerrors.Is(err, &conn_plugin.ValidationError{}),
		cerrors.Is(err, orchestrator.ErrInvalidPipelineConfig):
		return codes.InvalidArgument
	default:
		return codes.Internal
	}
}
