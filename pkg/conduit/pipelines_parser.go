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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"gopkg.in/yaml.v3"
	"io"
)

type PipelineConfig struct {
	Status string `yaml:"status"`
	Config struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"config"`
	Connectors map[string]struct {
		Type   string `yaml:"type"`
		Plugin string `yaml:"plugin"`
		Config struct {
			Name     string            `yaml:"name"`
			Settings map[string]string `yaml:"settings"`
		} `yaml:"config"`
		Processors map[string]struct {
			Name   string `yaml:"name"`
			Type   string `yaml:"type"`
			Config struct {
				Settings map[string]string `yaml:"settings"`
			} `yaml:"config"`
		} `yaml:"processors,omitempty"`
	} `yaml:"connectors,omitempty"`
	Processors map[string]struct {
		Name   string `yaml:"name"`
		Type   string `yaml:"type"`
		Config struct {
			Settings map[string]string `yaml:"settings"`
		} `yaml:"config"`
	} `yaml:"processors,omitempty"`
}

type PipelinesConfig struct {
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

func Parse(data io.Reader) (PipelinesConfig, error) {
	dec := yaml.NewDecoder(data)

	var docs []PipelinesConfig
	for {
		var doc PipelinesConfig
		err := dec.Decode(&doc)
		if err != nil && cerrors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return PipelinesConfig{}, cerrors.Errorf("error: %w", err)
		}
		docs = append(docs, doc)
	}
	merged := mergeMaps(docs)
	return merged, nil
}

// mergeMaps takes an array of PipelinesConfig and merges them into one map
func mergeMaps(arr []PipelinesConfig) PipelinesConfig {
	var mp PipelinesConfig
	pipelines := make(map[string]PipelineConfig, 0)

	for _, config := range arr {
		for k, v := range config.Pipelines {
			pipelines[k] = v
		}
	}
	mp.Pipelines = pipelines
	return mp
}
