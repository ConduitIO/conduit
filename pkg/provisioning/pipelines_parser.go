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
	"io"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"gopkg.in/yaml.v3"
)

const ParserVersion = "1.0"

var ErrUnsupportedVersion = cerrors.New("unsupported parser version")

type ProcessorConfig struct {
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
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
}

type PipelinesConfig struct {
	Version   string                    `yaml:"version"`
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

func Parse(data []byte) (map[string]PipelineConfig, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var docs []PipelinesConfig
	for {
		var doc PipelinesConfig
		err := dec.Decode(&doc)
		if cerrors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, cerrors.Errorf("parsing error: %w", err)
		}
		// check if version is empty
		if doc.Version == "" {
			return nil, cerrors.Errorf("version is empty: %w", ErrUnsupportedVersion)
		}
		// check if version is invalid
		if doc.Version != "1" && doc.Version != ParserVersion {
			return nil, cerrors.Errorf("version %s is not supported: %w", doc.Version, ErrUnsupportedVersion)
		}
		docs = append(docs, doc)
	}

	// empty file
	if len(docs) == 0 {
		return nil, nil
	}

	merged, err := mergePipelinesConfigMaps(docs)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// mergePipelinesConfigMaps takes an array of PipelinesConfig and merges them into one map
func mergePipelinesConfigMaps(arr []PipelinesConfig) (map[string]PipelineConfig, error) {
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
