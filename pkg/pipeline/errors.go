package pipeline

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"google.golang.org/grpc/codes"
)

var (
	ErrGracefulShutdown      = cerrors.New("graceful shutdown")
	ErrForceStop             = cerrors.New("force stop")
	ErrPipelineCannotRecover = cerrors.New("pipeline couldn't be recovered")

	ErrPipelineRunning          = cerrors.WithGRPCStatusCode(cerrors.New("pipeline is running"), codes.FailedPrecondition)
	ErrPipelineNotRunning       = cerrors.WithGRPCStatusCode(cerrors.New("pipeline not running"), codes.FailedPrecondition)
	ErrInstanceNotFound         = cerrors.WithGRPCStatusCode(cerrors.New("pipeline instance not found"), codes.NotFound)
	ErrNameMissing              = cerrors.WithGRPCStatusCode(cerrors.New("must provide a pipeline name"), codes.InvalidArgument)
	ErrIDMissing                = cerrors.WithGRPCStatusCode(cerrors.New("must provide a pipeline ID"), codes.InvalidArgument)
	ErrNameAlreadyExists        = cerrors.WithGRPCStatusCode(cerrors.New("pipeline name already exists"), codes.AlreadyExists)
	ErrInvalidCharacters        = cerrors.WithGRPCStatusCode(cerrors.New("pipeline ID contains invalid characters"), codes.InvalidArgument)
	ErrNameOverLimit            = cerrors.WithGRPCStatusCode(cerrors.New("pipeline name is over the character limit (128)"), codes.InvalidArgument)
	ErrIDOverLimit              = cerrors.WithGRPCStatusCode(cerrors.New("pipeline ID is over the character limit (128)"), codes.InvalidArgument)
	ErrDescriptionOverLimit     = cerrors.WithGRPCStatusCode(cerrors.New("pipeline description is over the character limit (8192)"), codes.InvalidArgument)
	ErrConnectorIDNotFound      = cerrors.WithGRPCStatusCode(cerrors.New("connector ID not found in pipeline"), codes.NotFound)
	ErrProcessorIDNotFound      = cerrors.WithGRPCStatusCode(cerrors.New("processor ID not found in pipeline"), codes.NotFound)
	ErrInsufficientConnectors   = cerrors.WithGRPCStatusCode(cerrors.New("pipeline must have at least one source and one destination connector to start"), codes.FailedPrecondition)
	ErrDLQPluginNotProvided     = cerrors.WithGRPCStatusCode(cerrors.New("DLQ plugin must be provided"), codes.InvalidArgument)
	ErrDLQWindowSizeNegative    = cerrors.WithGRPCStatusCode(cerrors.New("DLQ window size must be non-negative"), codes.InvalidArgument)
	ErrDLQWindowNackThresholdNegative = cerrors.WithGRPCStatusCode(cerrors.New("DLQ window nack threshold must be non-negative"), codes.InvalidArgument)
	ErrDLQWindowNackThresholdTooHigh = cerrors.WithGRPCStatusCode(cerrors.New("DLQ window nack threshold must be lower than window size"), codes.InvalidArgument)
)
