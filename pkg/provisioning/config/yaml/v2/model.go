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

const (
	LatestVersion = "2.2"
	MajorVersion  = "2"
)

// Changelog should be adjusted every time we change the pipeline config and add
// a new config version. Based on the changelog the parser will output warnings.
var Changelog = internal.Changelog{
	"2.0": {}, // initial version
	"2.1": {
		{
			Field:      "pipelines.*.processors.*.condition",
			ChangeType: internal.FieldIntroduced,
			Message:    "field condition was introduced in version 2.1, please update the pipeline config version",
		},
		{
			Field:      "pipelines.*.connectors.*.processors.*.condition",
			ChangeType: internal.FieldIntroduced,
			Message:    "field condition was introduced in version 2.1, please update the pipeline config version",
		},
	},
	"2.2": {
		{
			Field:      "pipelines.*.processors.*.plugin",
			ChangeType: internal.FieldIntroduced,
			Message:    "field plugin was introduced in version 2.2, please update the pipeline config version",
		},
		{
			Field:      "pipelines.*.connectors.*.processors.*.plugin",
			ChangeType: internal.FieldIntroduced,
			Message:    "field plugin was introduced in version 2.2, please update the pipeline config version",
		},
		{
			Field:      "pipelines.*.processors.*.type",
			ChangeType: internal.FieldDeprecated,
			Message:    "please use field 'plugin' (introduced in version 2.2)",
		},
		{
			Field:      "pipelines.*.connectors.*.processors.*.type",
			ChangeType: internal.FieldDeprecated,
			Message:    "please use field 'plugin' (introduced in version 2.2)",
		},
	},
}

type Configuration struct {
	Version   string     `yaml:"version" json:"version"`
	Pipelines []Pipeline `yaml:"pipelines" json:"pipelines"`
}

type Pipeline struct {
	ID          string      `yaml:"id" json:"id"`
	Status      string      `yaml:"status" json:"status"`
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description" json:"description"`
	Connectors  []Connector `yaml:"connectors" json:"connectors"`
	Processors  []Processor `yaml:"processors" json:"processors"`
	DLQ         DLQ         `yaml:"dead-letter-queue" json:"dead-letter-queue"`
}

type Connector struct {
	ID         string            `yaml:"id" json:"id"`
	Type       string            `yaml:"type" json:"type"`
	Plugin     string            `yaml:"plugin" json:"plugin"`
	Name       string            `yaml:"name" json:"name"`
	Settings   map[string]string `yaml:"settings" json:"settings"`
	Processors []Processor       `yaml:"processors" json:"processors"`
}

type Processor struct {
	ID        string            `yaml:"id" json:"id"`
	Type      string            `yaml:"type" json:"type"`
	Plugin    string            `yaml:"plugin" json:"plugin"`
	Condition string            `yaml:"condition" json:"condition"`
	Settings  map[string]string `yaml:"settings" json:"settings"`
	Workers   int               `yaml:"workers" json:"workers"`
}

type DLQ struct {
	Plugin              string            `yaml:"plugin" json:"plugin"`
	Settings            map[string]string `yaml:"settings" json:"settings"`
	WindowSize          *int              `yaml:"window-size" json:"window-size"`
	WindowNackThreshold *int              `yaml:"window-nack-threshold" json:"window-nack-threshold"`
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
		ID:         c.ID,
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
	plugin := p.Plugin
	if plugin == "" {
		plugin = p.Type
	}

	return config.Processor{
		ID:        p.ID,
		Plugin:    plugin,
		Settings:  p.Settings,
		Workers:   p.Workers,
		Condition: p.Condition,
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
