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

package v2

import (
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

func FromConfig(pipelines []config.Pipeline) Configuration {
	c := Configuration{
		Version:   LatestVersion,
		Pipelines: make([]Pipeline, len(pipelines)),
	}

	for i, p := range pipelines {
		c.Pipelines[i] = fromPipelineConfig(p)
	}

	return c
}

func fromPipelineConfig(p config.Pipeline) Pipeline {
	return Pipeline{
		ID:          p.ID,
		Status:      p.Status,
		Name:        p.Name,
		Description: p.Description,
		Connectors:  fromConnectorsConfig(p.Connectors),
		Processors:  fromProcessorsConfig(p.Processors),
		DLQ:         fromDLQConfig(p.DLQ),
	}
}

func fromConnectorsConfig(cc []config.Connector) []Connector {
	if len(cc) == 0 {
		return []Connector{}
	}

	connectors := make([]Connector, len(cc))

	for i, c := range cc {
		connectors[i] = Connector{
			ID:         c.ID,
			Type:       c.Type,
			Plugin:     c.Plugin,
			Name:       c.Name,
			Settings:   c.Settings,
			Processors: fromProcessorsConfig(c.Processors),
		}
	}

	return connectors
}

func fromProcessorsConfig(procs []config.Processor) []Processor {
	if len(procs) == 0 {
		return []Processor{}
	}

	processors := make([]Processor, len(procs))

	for i, proc := range procs {
		processors[i] = Processor{
			ID:       proc.ID,
			Plugin:   proc.Plugin,
			Settings: proc.Settings,
			Workers:  proc.Workers,
		}
	}

	return processors
}

func fromDLQConfig(dlq config.DLQ) DLQ {
	return DLQ{
		Plugin:              dlq.Plugin,
		Settings:            dlq.Settings,
		WindowSize:          dlq.WindowSize,
		WindowNackThreshold: dlq.WindowNackThreshold,
	}
}
