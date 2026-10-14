// Copyright © 2023 Meroxa, Inc.
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

package provisioning

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// Export takes a pipeline ID and exports its configuration. It either returns
// the exported configuration or pipeline.ErrInstanceNotFound, any other error
// points towards a corrupted state.
func (s *Service) Export(ctx context.Context, pipelineID string) (config.Pipeline, error) {
	return s.export(ctx, pipelineID, nil)
}

// export is Export that also records, in refs when it is non-nil, the stored
// config of every entity that carries SettingRefs. config.* cannot express
// SettingRefs, so importPipeline's rollback uses refs to put those entities
// back verbatim (Invariant 6). Entities without SettingRefs are not recorded.
func (s *Service) export(ctx context.Context, pipelineID string, refs *storedRefs) (config.Pipeline, error) {
	p, err := s.pipelineService.Get(ctx, pipelineID)
	if err != nil {
		return config.Pipeline{}, cerrors.Errorf("could not get pipeline with ID %v: %w", pipelineID, err)
	}

	if refs != nil && p.DLQ.HasSettingRefs() {
		refs.dlq = &savedDLQ{pipelineID: p.ID, dlq: p.DLQ}
	}
	pipConfig := s.pipelineToConfig(p)
	pipConfig.Connectors, err = s.exportConnectors(ctx, p.ConnectorIDs, refs)
	if err != nil {
		return config.Pipeline{}, cerrors.Errorf("could not extract connectors for pipeline with ID %v: %w", pipelineID, err)
	}
	pipConfig.Processors, err = s.exportProcessors(ctx, p.ProcessorIDs, refs)
	if err != nil {
		return config.Pipeline{}, cerrors.Errorf("could not extract processors for pipeline with ID %v: %w", pipelineID, err)
	}

	return pipConfig, nil
}

func (s *Service) exportConnectors(ctx context.Context, ids []string, refs *storedRefs) ([]config.Connector, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]config.Connector, len(ids))
	for i, connectorID := range ids {
		conn, err := s.connectorService.Get(ctx, connectorID)
		if err != nil {
			return nil, cerrors.Errorf("could not get connector with ID %v: %w", connectorID, err)
		}

		if refs != nil {
			conn.RLock()
			if conn.Config.HasSettingRefs() || conn.LastActiveConfig.HasSettingRefs() {
				refs.connectors = append(refs.connectors, savedConnector{id: conn.ID, plugin: conn.Plugin, cfg: conn.Config, lastActive: conn.LastActiveConfig})
			}
			conn.RUnlock()
		}
		conConfig := s.connectorToConfig(conn)
		conConfig.Processors, err = s.exportProcessors(ctx, conn.ProcessorIDs, refs)
		if err != nil {
			return nil, cerrors.Errorf("could not extract processors for connector with ID %v: %w", connectorID, err)
		}
		out[i] = conConfig
	}

	return out, nil
}

func (s *Service) exportProcessors(ctx context.Context, ids []string, refs *storedRefs) ([]config.Processor, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]config.Processor, len(ids))
	for i, processorID := range ids {
		proc, err := s.processorService.Get(ctx, processorID)
		if err != nil {
			return nil, cerrors.Errorf("could not get processor with ID %v: %w", processorID, err)
		}
		if refs != nil && proc.Config.HasSettingRefs() {
			refs.processors = append(refs.processors, savedProcessor{id: proc.ID, plugin: proc.Plugin, cfg: proc.Config})
		}
		out[i] = s.processorToConfig(proc)
	}

	return out, nil
}

func (s *Service) pipelineToConfig(p *pipeline.Instance) config.Pipeline {
	return config.Pipeline{
		ID: p.ID,
		// always export pipelines as stopped, otherwise the exported config
		// changes with the status of the pipeline
		Status:      config.StatusStopped,
		Name:        p.Config.Name,
		Description: p.Config.Description,
		Connectors:  nil, // extracted separately
		Processors:  nil, // extracted separately
		DLQ:         s.dlqToConfig(p.DLQ),
	}
}

func (*Service) dlqToConfig(dlq pipeline.DLQ) config.DLQ {
	return config.DLQ{
		Plugin:              dlq.Plugin,
		Settings:            dlq.Settings,
		WindowSize:          &dlq.WindowSize,
		WindowNackThreshold: &dlq.WindowNackThreshold,
	}
}

func (*Service) connectorToConfig(c *connector.Instance) config.Connector {
	return config.Connector{
		ID:         c.ID,
		Type:       strings.ToLower(c.Type.String()),
		Plugin:     c.Plugin,
		Name:       c.Config.Name,
		Settings:   c.Config.Settings,
		Processors: nil, // extracted separately
	}
}

func (*Service) processorToConfig(p *processor.Instance) config.Processor {
	return config.Processor{
		ID:       p.ID,
		Plugin:   p.Plugin,
		Settings: p.Config.Settings,
		Workers:  p.Config.Workers,
	}
}
