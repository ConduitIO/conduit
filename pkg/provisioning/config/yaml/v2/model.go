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
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml/internal"
)

// Changelog should be adjusted every time we change the pipeline config and add
// a new config version. Based on the changelog the parser will output warnings.
var Changelog = internal.Changelog{
	"2.0": {}, // initial version
}

type Configuration struct {
	Version   string     `yaml:"version"`
	Pipelines []Pipeline `yaml:"pipelines"`
}

type Pipeline struct {
	ID          string      `yaml:"id"`
	Status      string      `yaml:"status"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Connectors  []Connector `yaml:"connectors"`
	Processors  []Processor `yaml:"processors"`
	DLQ         DLQ         `yaml:"dead-letter-queue"`
}

type Connector struct {
	ID         string            `yaml:"id"`
	Type       string            `yaml:"type"`
	Plugin     string            `yaml:"plugin"`
	Name       string            `yaml:"name"`
	Settings   map[string]string `yaml:"settings"`
	Processors []Processor       `yaml:"processors"`
}

type Processor struct {
	ID       string            `yaml:"id"`
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
}

type DLQ struct {
	Plugin              string            `yaml:"plugin"`
	Settings            map[string]string `yaml:"settings"`
	WindowSize          *int              `yaml:"window-size"`
	WindowNackThreshold *int              `yaml:"window-nack-threshold"`
}

func (c Configuration) ToConfig() []config.Pipeline {
	if len(c.Pipelines) == 0 {
		return nil
	}

	out := make([]config.Pipeline, len(c.Pipelines))
	for i, pipeline := range c.Pipelines {
		out[i] = pipeline.ToConfig()
	}
	return out
}

func (p Pipeline) ToConfig() config.Pipeline {
	return config.Pipeline{
		ID:          p.ID,
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
	connectors := make([]config.Connector, len(p.Connectors))
	for i, connector := range p.Connectors {
		connectors[i] = connector.ToConfig()
	}
	return connectors
}
func (p Pipeline) processorsToConfig() []config.Processor {
	if len(p.Processors) == 0 {
		return nil
	}
	processors := make([]config.Processor, len(p.Processors))

	for i, processor := range p.Processors {
		processors[i] = processor.ToConfig()
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
	processors := make([]config.Processor, len(c.Processors))

	for i, processor := range c.Processors {
		processors[i] = processor.ToConfig()
	}
	return processors
}

func (p Processor) ToConfig() config.Processor {
	return config.Processor{
		ID:       p.ID,
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
