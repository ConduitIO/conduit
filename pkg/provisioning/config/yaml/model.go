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

package yaml

import "github.com/conduitio/conduit/pkg/provisioning/config"

type Configuration struct {
	Version   string              `yaml:"version"`
	Pipelines map[string]Pipeline `yaml:"pipelines"`
}

type Pipeline struct {
	Status      string               `yaml:"status"`
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Connectors  map[string]Connector `yaml:"connectors,omitempty"`
	Processors  map[string]Processor `yaml:"processors,omitempty"`
	DLQ         DLQ                  `yaml:"dead-letter-queue"`
}

type Connector struct {
	Type       string               `yaml:"type"`
	Plugin     string               `yaml:"plugin"`
	Name       string               `yaml:"name"`
	Settings   map[string]string    `yaml:"settings"`
	Processors map[string]Processor `yaml:"processors,omitempty"`
}

type Processor struct {
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
}

type DLQ struct {
	Plugin              string            `yaml:"plugin"`
	Settings            map[string]string `yaml:"settings"`
	WindowSize          *int              `yaml:"window-size"`
	WindowNackThreshold *int              `yaml:"window-nack-threshold"`
}

func (c Configuration) ToProvisioning() []config.Pipeline {
	if len(c.Pipelines) == 0 {
		return nil
	}

	out := make([]config.Pipeline, 0, len(c.Pipelines))
	for id, pipeline := range c.Pipelines {
		p := pipeline.ToProvisioning()
		p.ID = id
		out = append(out, p)
	}
	return out
}

func (p Pipeline) ToProvisioning() config.Pipeline {
	return config.Pipeline{
		Status:      p.Status,
		Name:        p.Name,
		Description: p.Description,
		Connectors:  p.connectorsToProvisioning(),
		Processors:  p.processorsToProvisioning(),
		DLQ:         p.DLQ.ToProvisioning(),
	}
}

func (p Pipeline) connectorsToProvisioning() []config.Connector {
	if len(p.Connectors) == 0 {
		return nil
	}
	connectors := make([]config.Connector, 0, len(p.Connectors))
	for id, connector := range p.Connectors {
		c := connector.ToProvisioning()
		c.ID = id
		connectors = append(connectors, c)
	}
	return connectors
}
func (p Pipeline) processorsToProvisioning() []config.Processor {
	if len(p.Processors) == 0 {
		return nil
	}
	processors := make([]config.Processor, 0, len(p.Processors))
	for id, processor := range p.Processors {
		proc := processor.ToProvisioning()
		proc.ID = id
		processors = append(processors, proc)
	}
	return processors
}

func (c Connector) ToProvisioning() config.Connector {
	return config.Connector{
		Type:       c.Type,
		Plugin:     c.Plugin,
		Name:       c.Name,
		Settings:   c.Settings,
		Processors: c.processorsToProvisioning(),
	}
}
func (c Connector) processorsToProvisioning() []config.Processor {
	if len(c.Processors) == 0 {
		return nil
	}
	processors := make([]config.Processor, 0, len(c.Processors))
	for id, processor := range c.Processors {
		proc := processor.ToProvisioning()
		proc.ID = id
		processors = append(processors, proc)
	}
	return processors
}

func (p Processor) ToProvisioning() config.Processor {
	return config.Processor{
		Type:     p.Type,
		Settings: p.Settings,
	}
}

func (p DLQ) ToProvisioning() config.DLQ {
	return config.DLQ{
		Plugin:              p.Plugin,
		Settings:            p.Settings,
		WindowSize:          p.WindowSize,
		WindowNackThreshold: p.WindowNackThreshold,
	}
}
