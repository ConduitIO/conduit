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

// Package conduit wires up everything under the hood of a Conduit instance
// including metrics, telemetry, logging, and server construction.
// It should only ever interact with the Orchestrator, never individual
// services. All of that responsibility should be left to the Orchestrator.
package conduit

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
)

type Pipelines []struct {
	Status string `yaml:"status"`
	Config struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"config"`
	Connectors []struct {
		Type   string `yaml:"type"`
		Plugin string `yaml:"plugin"`
		Config struct {
			Name     string      `yaml:"name"`
			Settings interface{} `yaml:"settings"`
		} `yaml:"config"`
	} `yaml:"connectors,omitempty"`
	Processors []struct {
		Name   string `yaml:"name"`
		Type   string `yaml:"type"`
		Parent struct {
			Type string `yaml:"type"`
			Name string `yaml:"name"`
		} `yaml:"parent"`
		Config struct {
			Settings interface{} `yaml:"settings"`
		} `yaml:"config"`
	}
}

func Parse(data []byte) ([]Pipelines, error) {
	//p := Pipelines{}
	//
	//err := yaml.Unmarshal(data, &p)
	//if err != nil {
	//	return nil, cerrors.Errorf("error: %w", err)
	//}
	//return &p, nil

	dec := yaml.NewDecoder(bytes.NewReader(data))

	var docs []Pipelines
	for {
		var doc Pipelines
		if err := dec.Decode(&doc); err != nil {
			fmt.Println("stopping because of error:", err)
			break
		}
		docs = append(docs, doc)
	}
	return docs, nil
}
