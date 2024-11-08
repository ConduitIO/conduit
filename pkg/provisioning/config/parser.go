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

package config

import (
	"context"
	"io"
)

type Pipeline struct {
	ID          string
	Status      string
	Name        string
	Description string
	Connectors  []Connector
	Processors  []Processor
	DLQ         DLQ
}

type Connector struct {
	ID         string
	Type       string
	Plugin     string
	Name       string
	Settings   map[string]string
	Processors []Processor
}

type Processor struct {
	ID        string
	Plugin    string
	Settings  map[string]string
	Workers   int
	Condition string
}

type DLQ struct {
	Plugin              string
	Settings            map[string]string
	WindowSize          *int
	WindowNackThreshold *int
}

// Classify fields as immutable, mutable or ignored. This is used by the
// provisioning logic to identify if an entity needs to be updated, recreated or
// if no action needs to be taken in case a certain field is updated. All fields
// except "ID" need to be added to exactly one slice.
var (
	PipelineMutableFields = []string{"Name", "Description", "Connectors", "Processors", "DLQ"}
	PipelineIgnoredFields = []string{"Status"}

	ConnectorImmutableFields = []string{"Type"}
	ConnectorMutableFields   = []string{"Name", "Settings", "Processors", "Plugin"}
)

// Parser reads data from reader and parses all pipelines defined in the
// configuration.
type Parser interface {
	Parse(ctx context.Context, reader io.Reader) ([]Pipeline, error)
}
