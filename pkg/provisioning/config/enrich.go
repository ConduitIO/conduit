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

// Enrich sets default values for pipeline config fields
func Enrich(cfg Pipeline) Pipeline {
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if cfg.Status == "" {
		cfg.Status = StatusRunning
	}

	cfg.Connectors = enrichConnectors(cfg.Connectors, cfg.ID)
	cfg.Processors = enrichProcessors(cfg.Processors, cfg.ID)
	return cfg
}

// enrichConnectors sets default values for connectors config fields
func enrichConnectors(mp []Connector, pipelineID string) []Connector {
	out := make([]Connector, len(mp))
	for i, cfg := range mp {
		if cfg.Name == "" {
			cfg.Name = cfg.ID
		}
		// attach the pipelineID to the connectorID
		cfg.ID = pipelineID + ":" + cfg.ID
		cfg.Processors = enrichProcessors(cfg.Processors, cfg.ID)
		out[i] = cfg
	}
	return out
}

// enrichProcessorsConfig sets default values for processors config fields
func enrichProcessors(mp []Processor, parentID string) []Processor {
	out := make([]Processor, len(mp))
	for i, cfg := range mp {
		cfg.ID = parentID + ":" + cfg.ID
		out[i] = cfg
	}
	return out
}
