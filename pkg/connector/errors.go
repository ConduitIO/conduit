package connector

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrInstanceNotFound          = cerrors.New("connector instance not found")
	ErrInvalidConnectorType      = cerrors.New("invalid connector type")
	ErrNameMissing               = cerrors.New("must provide a connector name")
	ErrIDMissing                 = cerrors.New("must provide a connector ID")
	ErrInvalidCharacters         = cerrors.New("connector ID contains invalid characters")
	ErrNameOverLimit             = cerrors.New("connector name is over the character limit (256)")
	ErrIDOverLimit               = cerrors.New("connector ID is over the character limit (256)")
	ErrProcessorIDNotFound       = cerrors.New("processor ID not found")
	ErrInvalidConnectorStateType = cerrors.New("invalid connector state type")
	ErrConnectorRunning          = cerrors.New("connector is running")
	ErrPluginMissing             = cerrors.New("must provide a plugin")
	ErrPipelineIDMissing         = cerrors.New("must provide a pipeline ID")
	ErrInvalidConnectorConfig    = cerrors.New("invalid connector configuration")
	ErrNameAlreadyExists         = cerrors.New("connector name already exists")
)
