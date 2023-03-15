// Copyright © 2022 Meroxa, Inc.
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
	"bytes"
	"context"
	"io"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/yaml/v3"
)

type ProcessorConfig struct {
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
	Workers  int               `yaml:"workers"`
}

type ConnectorConfig struct {
	Type       string                     `yaml:"type"`
	Plugin     string                     `yaml:"plugin"`
	Name       string                     `yaml:"name"`
	Settings   map[string]string          `yaml:"settings"`
	Processors map[string]ProcessorConfig `yaml:"processors,omitempty"`
}

type PipelineConfig struct {
	Status      string                     `yaml:"status"`
	Name        string                     `yaml:"name"`
	Description string                     `yaml:"description"`
	Connectors  map[string]ConnectorConfig `yaml:"connectors,omitempty"`
	Processors  map[string]ProcessorConfig `yaml:"processors,omitempty"`
	DLQ         DLQConfig                  `yaml:"dead-letter-queue"`
}

type DLQConfig struct {
	Plugin              string            `yaml:"plugin"`
	Settings            map[string]string `yaml:"settings"`
	WindowSize          *int              `yaml:"window-size"`
	WindowNackThreshold *int              `yaml:"window-nack-threshold"`
}

type PipelinesConfig struct {
	Version   string                    `yaml:"version"`
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

type Parser struct {
	logger log.CtxLogger
}

func NewParser(logger log.CtxLogger) *Parser {
	return &Parser{
		logger: logger.WithComponent("provisioning.Parser"),
	}
}

func (p *Parser) Parse(ctx context.Context, path string, data []byte) (map[string]PipelineConfig, error) {
	// replace environment variables with their values
	data = []byte(os.ExpandEnv(string(data)))
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var docs []PipelinesConfig
	for {
		var doc PipelinesConfig
		linter := newConfigLinter()
		dec.WithHook(linter.DecoderHook) // register fresh linter hook
		err := dec.Decode(&doc)
		if err != nil {
			// we reached the end of the document
			if cerrors.Is(err, io.EOF) {
				break
			}
			// check if it's a type error (document was partially decoded)
			var typeErr *yaml.TypeError
			if cerrors.As(err, &typeErr) {
				err = p.handleYamlTypeError(ctx, path, typeErr)
			}
			// check if we recovered from the error
			if err != nil {
				return nil, cerrors.Errorf("parsing error: %w", err)
			}
		}
		linter.LogWarnings(ctx, p.logger, path)
		docs = append(docs, doc)
	}

	// empty file
	if len(docs) == 0 {
		return nil, nil
	}

	merged, err := p.mergePipelinesConfigMaps(docs)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

func (p *Parser) handleYamlTypeError(ctx context.Context, path string, typeErr *yaml.TypeError) error {
	for _, uerr := range typeErr.Errors {
		if _, ok := uerr.(*yaml.UnknownFieldError); !ok {
			// we don't tolerate any other error except unknown field
			return typeErr
		}
	}
	// only UnknownFieldErrors found, log them
	for _, uerr := range typeErr.Errors {
		e := p.logger.Warn(ctx).
			Int("line", uerr.Line()).
			Int("column", uerr.Column())
		if path != "" {
			e.Str("path", path)
		}
		e.Msg(uerr.Error())
	}
	return nil
}

// mergePipelinesConfigMaps takes an array of PipelinesConfig and merges them into one map
func (p *Parser) mergePipelinesConfigMaps(arr []PipelinesConfig) (map[string]PipelineConfig, error) {
	pipelines := make(map[string]PipelineConfig, 0)

	for _, config := range arr {
		for k, v := range config.Pipelines {
			if _, ok := pipelines[k]; ok {
				return nil, cerrors.Errorf("found a duplicated pipeline id: %s", k)
			}
			pipelines[k] = v
		}
	}
	return pipelines, nil
}
