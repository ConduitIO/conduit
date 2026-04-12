package orchestrator

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrPipelineHasConnectorsAttached    = cerrors.New("pipeline has connectors attached")
	ErrPipelineHasProcessorsAttached    = cerrors.New("pipeline has processors attached")
	ErrConnectorHasProcessorsAttached   = cerrors.New("connector has processors attached")
	ErrImmutableProvisionedByConfig     = cerrors.New("resource provisioned by config cannot be changed via API")
	ErrInvalidProcessorParentType       = cerrors.New("invalid processor parent type")
	ErrInvalidPipelineConfig            = cerrors.New("invalid pipeline configuration")
)
