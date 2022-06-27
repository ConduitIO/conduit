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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"gopkg.in/yaml.v3"
)

const ParserVersion = "1.0"

type ProcessorConfig struct {
	Settings map[string]string `yaml:"settings"`
}

type ProcessorInfo map[string]struct {
	Name   string          `yaml:"name"`
	Type   string          `yaml:"type"`
	Config ProcessorConfig `yaml:"config"`
}

type ConnectorConfig struct {
	Name     string            `yaml:"name"`
	Settings map[string]string `yaml:"settings"`
}

type ConnectorInfo map[string]struct {
	Type       string          `yaml:"type"`
	Plugin     string          `yaml:"plugin"`
	Config     ConnectorConfig `yaml:"config"`
	Processors ProcessorInfo   `yaml:"processors,omitempty"`
}

type PipelineConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type PipelineInfo struct {
	Status     string         `yaml:"status"`
	Config     PipelineConfig `yaml:"config"`
	Connectors ConnectorInfo  `yaml:"connectors,omitempty"`
	Processors ProcessorInfo  `yaml:"processors,omitempty"`
}

type PipelinesInfo struct {
	Version   string                  `yaml:"version"`
	Pipelines map[string]PipelineInfo `yaml:"pipelines"`
}

func Parse(data []byte) (PipelinesInfo, error) {
	// replace environment variables with their values
	data = []byte(os.ExpandEnv(string(data)))
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var docs []PipelinesInfo
	for {
		var doc PipelinesInfo
		err := dec.Decode(&doc)
		if err != nil && cerrors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return PipelinesInfo{}, cerrors.Errorf("parsing error: %w", err)
		}
		// check if version is empty
		if doc.Version == "" {
			return PipelinesInfo{}, cerrors.New("version field is not specified")
		}
		// check if version is invalid
		if doc.Version != "1" && doc.Version != ParserVersion {
			return PipelinesInfo{}, cerrors.Errorf("invalid version : %s , try version %s instead", doc.Version, ParserVersion)
		}
		docs = append(docs, doc)
	}

	merged, err := mergeMaps(docs)
	if err != nil {
		return PipelinesInfo{}, err
	}
	return merged, nil
}

// mergeMaps takes an array of PipelinesInfo and merges them into one map
func mergeMaps(arr []PipelinesInfo) (PipelinesInfo, error) {
	var mp PipelinesInfo
	pipelines := make(map[string]PipelineInfo, 0)

	for _, config := range arr {
		for k, v := range config.Pipelines {
			if _, ok := pipelines[k]; ok {
				return PipelinesInfo{}, cerrors.Errorf("found a duplicated pipeline id: %s", k)
			}
			pipelines[k] = v
		}
	}
	mp.Version = ParserVersion
	mp.Pipelines = pipelines
	return mp, nil
}
