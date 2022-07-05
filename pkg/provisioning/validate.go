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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
)

var (
	ErrMandatoryField = cerrors.New("mandatory field not specified")
	ErrInvalidField   = cerrors.New("invalid field value")
)

const (
	StatusRunning   = "running"
	StatusStopped   = "stopped"
	TypeConnector   = "source"
	TypeDestination = "destination"
)

// ValidatePipelinesConfig validates config field values for a pipeline
func ValidatePipelinesConfig(cfg PipelineConfig) error {
	var err, tmpErr error
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		err = multierror.Append(err, cerrors.Errorf("\"status\" is invalid: %w", ErrInvalidField))
	}
	tmpErr = validateConnectorsConfig(cfg.Connectors)
	if tmpErr != nil {
		err = multierror.Append(err, tmpErr)
	}
	tmpErr = validateProcessorsConfig(cfg.Processors)
	if tmpErr != nil {
		err = multierror.Append(err, tmpErr)
	}
	return err
}

// validateConnectorsConfig validates config field values for connectors
func validateConnectorsConfig(mp map[string]ConnectorConfig) error {
	var err, pErr error
	for k, cfg := range mp {
		if cfg.Plugin == "" {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"plugin\" is mandatory: %w", k, ErrMandatoryField))
		}
		if cfg.Type == "" {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"type\" is mandatory: %w", k, ErrMandatoryField))
		}
		if cfg.Type != "" && cfg.Type != TypeConnector && cfg.Type != TypeDestination {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"type\" is invalid: %w", k, ErrInvalidField))
		}

		pErr = validateProcessorsConfig(cfg.Processors)
		if pErr != nil {
			err = multierror.Append(err, cerrors.Errorf("connector %q: %w", k, pErr))
		}
		mp[k] = cfg
	}
	return err
}

// validateProcessorsConfig validates config field values for processors
func validateProcessorsConfig(mp map[string]ProcessorConfig) error {
	for k, cfg := range mp {
		if cfg.Type == "" {
			return cerrors.Errorf("processors %q: \"type\" is mandatory: %w", k, ErrMandatoryField)
		}
		mp[k] = cfg
	}
	return nil
}
