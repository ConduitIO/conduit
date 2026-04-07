package pipeline

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

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

	// ErrInvalidCharacters is returned when a pipeline ID contains invalid characters.
	ErrInvalidCharacters = fmt.Errorf("pipeline ID contains invalid characters, allowed characters: %s", idRegex.String())
	// ErrNameOverLimit is returned when a pipeline name is over the character limit.
	ErrNameOverLimit = fmt.Errorf("pipeline name is over the character limit (%d)", NameLengthLimit)
	// ErrIDOverLimit is returned when a pipeline ID is over the character limit.
	ErrIDOverLimit = fmt.Errorf("pipeline ID is over the character limit (%d)", IDLengthLimit)
	// ErrDescriptionOverLimit is returned when a pipeline description is over the character limit.
	ErrDescriptionOverLimit = fmt.Errorf("pipeline description is over the character limit (%d)", DescriptionLengthLimit)

	ErrConnectorIDNotFound = cerrors.New("connector ID not found in pipeline")
	ErrProcessorIDNotFound = cerrors.New("processor ID not found in pipeline")

	// ErrInvalidPipelineStructure is returned when a pipeline has an invalid structure,
	// e.g. trying to start a pipeline without any source connectors.
	ErrInvalidPipelineStructure = cerrors.New("invalid pipeline structure")
	// ErrInvalidDLQConfig is returned when the DLQ configuration is invalid.
	ErrInvalidDLQConfig = cerrors.New("invalid DLQ configuration")
)
