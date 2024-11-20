package pipelines

import "github.com/conduitio/conduit-commons/config"

type connectorTemplate struct {
	Name   string
	Params config.Parameters
}

type pipelineTemplate struct {
	Name            string
	SourceSpec      connectorTemplate
	DestinationSpec connectorTemplate
}
