// Copyright © 2024 Meroxa, Inc.
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

package avro

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/internal"
)

type preRegisteredConfig struct {
	// The subject of the schema in the schema registry used to encode the record.
	Subject string `json:"subject"`
	// The version of the schema in the schema registry used to encode the record.
	Version int `json:"version" validate:"gt=0"`
}

type schemaConfig struct {
	// Strategy to use to determine the schema for the record.
	// Available strategies are:
	// * `preRegistered` (recommended) - Download an existing schema from the schema registry.
	//    This strategy is further configured with options starting with `schema.preRegistered.*`.
	// * `autoRegister` (for development purposes) - Infer the schema from the record and register it
	//    in the schema registry. This strategy is further configured with options starting with
	//   `schema.autoRegister.*`.
	//
	// For more information about the behavior of each strategy read the main processor description.
	StrategyType string `json:"strategy" validate:"required,inclusion=preRegistered|autoRegister"`

	PreRegistered preRegisteredConfig `json:"preRegistered"`

	// The subject name under which the inferred schema will be registered in the schema registry.
	AutoRegisteredSubject string `json:"autoRegister.subject"`

	strategy internal.SchemaStrategy
}

func (c *schemaConfig) parse() error {
	switch c.StrategyType {
	case "preRegistered":
		return c.parsePreRegistered()
	case "autoRegister":
		return c.parseAutoRegister()
	default:
		return cerrors.Errorf("unknown schema strategy %q", c.StrategyType)
	}
}

func (c *schemaConfig) parsePreRegistered() error {
	if c.PreRegistered.Subject == "" {
		return cerrors.New("subject required for schema strategy 'preRegistered'")
	}
	// TODO allow version to be set to "latest"
	if c.PreRegistered.Version <= 0 {
		return cerrors.Errorf("version needs to be positive: %v", c.PreRegistered.Version)
	}

	c.strategy = internal.DownloadSchemaStrategy{
		Subject: c.PreRegistered.Subject,
		Version: c.PreRegistered.Version,
	}
	return nil
}

func (c *schemaConfig) parseAutoRegister() error {
	if c.AutoRegisteredSubject == "" {
		return cerrors.New("subject required for schema strategy 'autoRegister'")
	}

	c.strategy = internal.ExtractAndUploadSchemaStrategy{
		Subject: c.AutoRegisteredSubject,
	}
	return nil
}
