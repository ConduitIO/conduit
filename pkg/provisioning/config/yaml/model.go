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

type Configurations []Configuration

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

func (c Configurations) ToConfig() []config.Pipeline {
	if len(c) == 0 {
		return nil
	}

	out := make([]config.Pipeline, 0, len(c))
	for _, cfg := range c {
		out = append(out, cfg.ToConfig()...)
	}
	return out
}

func (c Configuration) ToConfig() []config.Pipeline {
	if len(c.Pipelines) == 0 {
		return nil
	}

	out := make([]config.Pipeline, 0, len(c.Pipelines))
	for id, pipeline := range c.Pipelines {
		p := pipeline.ToConfig()
		p.ID = id
		out = append(out, p)
	}
	return out
}

func (p Pipeline) ToConfig() config.Pipeline {
	return config.Pipeline{
		Status:      p.Status,
		Name:        p.Name,
		Description: p.Description,
		Connectors:  p.connectorsToConfig(),
		Processors:  p.processorsToConfig(),
		DLQ:         p.DLQ.ToConfig(),
	}
}

func (p Pipeline) connectorsToConfig() []config.Connector {
	if len(p.Connectors) == 0 {
		return nil
	}
	connectors := make([]config.Connector, 0, len(p.Connectors))
	for id, connector := range p.Connectors {
		c := connector.ToConfig()
		c.ID = id
		connectors = append(connectors, c)
	}
	return connectors
}
func (p Pipeline) processorsToConfig() []config.Processor {
	if len(p.Processors) == 0 {
		return nil
	}
	processors := make([]config.Processor, 0, len(p.Processors))

	// Warning: this ordering is not deterministic, v2 of the pipeline config
	// fixes this.
	for id, processor := range p.Processors {
		proc := processor.ToConfig()
		proc.ID = id
		processors = append(processors, proc)
	}
	return processors
}

func (c Connector) ToConfig() config.Connector {
	return config.Connector{
		Type:       c.Type,
		Plugin:     c.Plugin,
		Name:       c.Name,
		Settings:   c.Settings,
		Processors: c.processorsToConfig(),
	}
}
func (c Connector) processorsToConfig() []config.Processor {
	if len(c.Processors) == 0 {
		return nil
	}
	processors := make([]config.Processor, 0, len(c.Processors))

	// Warning: this ordering is not deterministic, v2 of the pipeline config
	// fixes this.
	for id, processor := range c.Processors {
		proc := processor.ToConfig()
		proc.ID = id
		processors = append(processors, proc)
	}
	return processors
}

func (p Processor) ToConfig() config.Processor {
	return config.Processor{
		Type:     p.Type,
		Settings: p.Settings,
	}
}

func (p DLQ) ToConfig() config.DLQ {
	return config.DLQ{
		Plugin:              p.Plugin,
		Settings:            p.Settings,
		WindowSize:          p.WindowSize,
		WindowNackThreshold: p.WindowNackThreshold,
	}
}
