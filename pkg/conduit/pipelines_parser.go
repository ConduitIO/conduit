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

package conduit

import (
	"bytes"
	"io"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"gopkg.in/yaml.v3"
)

const ParserVersion = "1.0"

var ErrUnsupportedVersion = cerrors.New("unsupported parser version")

type ProcessorConfig map[string]struct {
	Name     string            `yaml:"name"`
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
}

type ConnectorConfig map[string]struct {
	Type       string            `yaml:"type"`
	Plugin     string            `yaml:"plugin"`
	Name       string            `yaml:"name"`
	Settings   map[string]string `yaml:"settings"`
	Processors ProcessorConfig   `yaml:"processors,omitempty"`
}

type PipelineConfig struct {
	Status      string          `yaml:"status"`
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Connectors  ConnectorConfig `yaml:"connectors,omitempty"`
	Processors  ProcessorConfig `yaml:"processors,omitempty"`
}

type PipelinesConfig struct {
	Version   string                    `yaml:"version"`
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

func Parse(data []byte) (PipelinesConfig, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var docs []PipelinesConfig
	for {
		var doc PipelinesConfig
		err := dec.Decode(&doc)
		if cerrors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return PipelinesConfig{}, cerrors.Errorf("parsing error: %w", err)
		}
		// check if version is empty
		if doc.Version == "" {
			return PipelinesConfig{}, cerrors.Errorf("version is empty: %w", ErrUnsupportedVersion)
		}
		// check if version is invalid
		if doc.Version != "1" && doc.Version != ParserVersion {
			return PipelinesConfig{}, cerrors.Errorf("version %s is not supported : %w", doc.Version, ErrUnsupportedVersion)
		}
		docs = append(docs, doc)
	}

	// empty file
	if len(docs) == 0 {
		return PipelinesConfig{}, nil
	}

	merged, err := mergeMaps(docs)
	if err != nil {
		return PipelinesConfig{}, err
	}
	return merged, nil
}

// mergeMaps takes an array of PipelinesConfig and merges them into one map
func mergeMaps(arr []PipelinesConfig) (PipelinesConfig, error) {
	var mp PipelinesConfig
	pipelines := make(map[string]PipelineConfig, 0)

	for _, config := range arr {
		for k, v := range config.Pipelines {
			if _, ok := pipelines[k]; ok {
				return PipelinesConfig{}, cerrors.Errorf("found a duplicated pipeline id: %s", k)
			}
			pipelines[k] = v
		}
	}
	mp.Version = ParserVersion
	mp.Pipelines = pipelines
	return mp, nil
}
