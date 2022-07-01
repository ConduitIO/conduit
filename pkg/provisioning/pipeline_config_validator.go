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

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrMandatoryField = cerrors.New("mandatory field not specified")
	ErrInvalidField   = cerrors.New("invalid field value")
)

// ValidatePipelinesConfig sets default values for empty fields and validates config field values for pipelines
func ValidatePipelinesConfig(mp map[string]PipelineConfig) (map[string]PipelineConfig, error) {
	var err error
	mp = enrichPipelinesConfig(mp)
	for k, cfg := range mp {
		cfg.Connectors, err = validateConnectorsConfig(cfg.Connectors)
		if err != nil {
			return nil, cerrors.Errorf("pipeline `%s`: %w", k, err)
		}
		cfg.Processors, err = validateProcessorsConfig(cfg.Processors)
		if err != nil {
			return nil, cerrors.Errorf("pipeline `%s`: %w", k, err)
		}
		mp[k] = cfg
	}
	return mp, nil
}

// validateConnectorsConfig validates config field values for connectors
func validateConnectorsConfig(mp map[string]ConnectorConfig) (map[string]ConnectorConfig, error) {
	var err error
	for k, cfg := range mp {
		if cfg.Plugin == "" {
			return nil, cerrors.Errorf("connector `%s`: `plugin` is mandatory: %w", k, ErrMandatoryField)
		}
		if cfg.Type == "" {
			return nil, cerrors.Errorf("connector `%s`: `type` is mandatory: %w", k, ErrMandatoryField)
		}
		if cfg.Type != "source" && cfg.Type != "destination" {
			return nil, cerrors.Errorf("connector `%s`: `type` is invalid: %w", k, ErrInvalidField)
		}
		// should we validate that `settings` map is not empty?

		cfg.Processors, err = validateProcessorsConfig(cfg.Processors)
		if err != nil {
			return nil, cerrors.Errorf("connector `%s`: %w", k, err)
		}
		mp[k] = cfg
	}
	return mp, nil
}

// validateProcessorsConfig validates config field values for processors
func validateProcessorsConfig(mp map[string]ProcessorConfig) (map[string]ProcessorConfig, error) {
	for k, cfg := range mp {
		if cfg.Type == "" {
			return nil, cerrors.Errorf("processors `%s`: `type` is mandatory: %w", k, ErrMandatoryField)
		}
		mp[k] = cfg
	}
	return mp, nil
}

// enrichPipelinesConfig sets default values for pipeline config fields
func enrichPipelinesConfig(mp map[string]PipelineConfig) map[string]PipelineConfig {
	for k, cfg := range mp {
		if cfg.Name == "" {
			cfg.Name = k
		}
		if cfg.Status != "stopped" {
			cfg.Status = "running"
		}
		cfg.Connectors = enrichConnectorsConfig(cfg.Connectors)
		mp[k] = cfg
	}
	return mp
}

// enrichConnectorsConfig sets default values for connectors config fields
func enrichConnectorsConfig(mp map[string]ConnectorConfig) map[string]ConnectorConfig {
	for k, cfg := range mp {
		if cfg.Name == "" {
			cfg.Name = k
		}
		mp[k] = cfg
	}
	return mp
}
