package pipeline

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrGracefulShutdown      = cerrors.New("graceful shutdown")
	ErrForceStop             = cerrors.New("force stop")
	ErrPipelineCannotRecover = cerrors.New("pipeline couldn't be recovered")
	ErrPipelineRunning       = cerrors.New("pipeline is running")
	ErrPipelineNotRunning    = cerrors.New("pipeline not running")
	ErrInstanceNotFound      = cerrors.New("pipeline instance not found")
	ErrNameMissing           = cerrors.New("must provide a pipeline name")
	ErrIDMissing             = cerrors.New("must provide a pipeline ID")
	ErrNameAlreadyExists     = cerrors.New("pipeline name already exists")
	ErrInvalidCharacters     = cerrors.New("pipeline ID contains invalid characters")
	ErrNameOverLimit         = cerrors.New("pipeline name is over the character limit (64)")
	ErrIDOverLimit           = cerrors.New("pipeline ID is over the character limit (64)")
	ErrDescriptionOverLimit  = cerrors.New("pipeline description is over the character limit (8192)")
	ErrConnectorIDNotFound   = cerrors.New("connector ID not found")
	ErrProcessorIDNotFound   = cerrors.New("processor ID not found")

	// ErrNoSourceConnectors indicates that a pipeline cannot be started because it has no source connectors.
	ErrNoSourceConnectors = cerrors.New("pipeline has no source connectors")
)
