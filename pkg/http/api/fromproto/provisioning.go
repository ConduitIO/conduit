// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fromproto

import (
	"github.com/conduitio/conduit/pkg/provisioning/config"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

// PipelineDocument converts the API's whole-pipeline-document proto shape
// into pkg/provisioning/config.Pipeline — the same struct
// deploy.ParseSinglePipeline produces from a YAML file, so
// PipelineAPIv1.PlanPipeline/ApplyPipeline hand provisioning.Service exactly
// what the CLI standalone path hands it, and the two surfaces can't drift on
// what a "desired pipeline" means. Unlike PipelineConfig (Pipeline.Config,
// name/description only), this carries connectors/processors/DLQ inline.
func PipelineDocument(in *apiv1.PipelineDocument) config.Pipeline {
	if in == nil {
		return config.Pipeline{}
	}

	connectors := make([]config.Connector, len(in.Connectors))
	for i, c := range in.Connectors {
		connectors[i] = pipelineDocumentConnector(c)
	}
	processors := make([]config.Processor, len(in.Processors))
	for i, p := range in.Processors {
		processors[i] = pipelineDocumentProcessor(p)
	}

	return config.Pipeline{
		ID:          in.Id,
		Status:      in.Status,
		Name:        in.Name,
		Description: in.Description,
		Connectors:  connectors,
		Processors:  processors,
		DLQ:         pipelineDocumentDLQ(in.Dlq),
	}
}

func pipelineDocumentConnector(in *apiv1.PipelineDocument_Connector) config.Connector {
	if in == nil {
		return config.Connector{}
	}
	processors := make([]config.Processor, len(in.Processors))
	for i, p := range in.Processors {
		processors[i] = pipelineDocumentProcessor(p)
	}
	return config.Connector{
		ID:         in.Id,
		Type:       in.Type,
		Plugin:     in.Plugin,
		Name:       in.Name,
		Settings:   in.Settings,
		Processors: processors,
	}
}

func pipelineDocumentProcessor(in *apiv1.PipelineDocument_Processor) config.Processor {
	if in == nil {
		return config.Processor{}
	}
	return config.Processor{
		ID:        in.Id,
		Plugin:    in.Plugin,
		Settings:  in.Settings,
		Workers:   int(in.Workers),
		Condition: in.Condition,
	}
}

// pipelineDocumentDLQ converts in to a config.DLQ with non-nil WindowSize/
// WindowNackThreshold pointers (a zero value is a real, meaningful "0", not
// "unset" — see config.Enrich, which config.Pipeline always expects to have
// already run over a value like this one by the time provisioning.Service
// sees it: PipelineAPIv1's handlers run it explicitly before calling
// Plan/ApplyPlanLive, same as deploy.ParseSinglePipeline does for the CLI).
func pipelineDocumentDLQ(in *apiv1.PipelineDocument_DLQ) config.DLQ {
	if in == nil {
		return config.DLQ{}
	}
	windowSize := int(in.WindowSize)                   //nolint:gosec // no risk of overflow
	windowNackThreshold := int(in.WindowNackThreshold) //nolint:gosec // no risk of overflow
	return config.DLQ{
		Plugin:              in.Plugin,
		Settings:            in.Settings,
		WindowSize:          &windowSize,
		WindowNackThreshold: &windowNackThreshold,
	}
}
