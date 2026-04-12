package connector

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"google.golang.org/grpc/codes"
)

var (
	ErrInstanceNotFound          = cerrors.WithGRPCStatusCode(cerrors.New("connector instance not found"), codes.NotFound)
	ErrNameMissing               = cerrors.WithGRPCStatusCode(cerrors.New("must provide a connector name"), codes.InvalidArgument)
	ErrNameOverLimit             = cerrors.WithGRPCStatusCode(cerrors.New("connector name is over the character limit (256)"), codes.InvalidArgument)
	ErrIDMissing                 = cerrors.WithGRPCStatusCode(cerrors.New("must provide a connector ID"), codes.InvalidArgument)
	ErrIDOverLimit               = cerrors.WithGRPCStatusCode(cerrors.New("connector ID is over the character limit (256)"), codes.InvalidArgument)
	ErrInvalidCharacters         = cerrors.WithGRPCStatusCode(cerrors.New("connector ID contains invalid characters"), codes.InvalidArgument)
	ErrPluginNotProvided         = cerrors.WithGRPCStatusCode(cerrors.New("must provide a plugin"), codes.InvalidArgument)
	ErrPipelineIDNotProvided     = cerrors.WithGRPCStatusCode(cerrors.New("must provide a pipeline ID"), codes.InvalidArgument)
	ErrInvalidConnectorType      = cerrors.WithGRPCStatusCode(cerrors.New("invalid connector type"), codes.InvalidArgument)
	ErrInvalidConnectorStateType = cerrors.WithGRPCStatusCode(cerrors.New("invalid connector state type"), codes.InvalidArgument)
	ErrProcessorIDNotFound       = cerrors.WithGRPCStatusCode(cerrors.New("processor ID not found in connector"), codes.NotFound)
)
