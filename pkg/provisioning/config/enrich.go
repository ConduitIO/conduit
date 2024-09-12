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

package config

import "github.com/conduitio/conduit/pkg/pipeline"

// Enrich sets default values for pipeline config fields
func Enrich(cfg Pipeline) Pipeline {
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if cfg.Status == "" {
		cfg.Status = StatusRunning
	}

	cfg.DLQ = enrichDLQ(cfg.DLQ)
	cfg.Connectors = enrichConnectors(cfg.Connectors, cfg.ID)
	cfg.Processors = enrichProcessors(cfg.Processors, cfg.ID)
	return cfg
}

func enrichDLQ(dlq DLQ) DLQ {
	if dlq.Plugin == "" {
		dlq.Plugin = pipeline.DefaultDLQ.Plugin
	}
	if dlq.Settings == nil {
		dlq.Settings = make(map[string]string, len(pipeline.DefaultDLQ.Settings))
		for k, v := range pipeline.DefaultDLQ.Settings {
			dlq.Settings[k] = v
		}
	}
	if dlq.WindowSize == nil {
		tmp := pipeline.DefaultDLQ.WindowSize
		dlq.WindowSize = &tmp
	}
	if dlq.WindowNackThreshold == nil {
		tmp := pipeline.DefaultDLQ.WindowNackThreshold
		dlq.WindowNackThreshold = &tmp
	}
	return dlq
}

// enrichConnectors sets default values for connectors config fields
func enrichConnectors(mp []Connector, pipelineID string) []Connector {
	if mp == nil {
		return nil
	}
	out := make([]Connector, len(mp))
	for i, cfg := range mp {
		if cfg.Name == "" {
			cfg.Name = cfg.ID
		}
		if cfg.Settings == nil {
			cfg.Settings = make(map[string]string)
		}
		if cfg.ID != "" {
			// attach the pipelineID to the connectorID only if the connectorID
			// is actually provided, otherwise the validation will miss it later
			cfg.ID = pipelineID + ":" + cfg.ID
		}
		cfg.Processors = enrichProcessors(cfg.Processors, cfg.ID)
		out[i] = cfg
	}
	return out
}

// enrichProcessorsConfig sets default values for processors config fields
func enrichProcessors(mp []Processor, parentID string) []Processor {
	if mp == nil {
		return nil
	}
	out := make([]Processor, len(mp))
	for i, cfg := range mp {
		if cfg.Workers == 0 {
			cfg.Workers = 1
		}
		if cfg.Settings == nil {
			cfg.Settings = make(map[string]string)
		}
		if cfg.ID != "" {
			// attach the parentID to the processorID only if the processorID
			// is actually provided, otherwise the validation will miss it later
			cfg.ID = parentID + ":" + cfg.ID
		}
		out[i] = cfg
	}
	return out
}
