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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
)

const (
	StatusRunning   = "running"
	StatusStopped   = "stopped"
	TypeSource      = "source"
	TypeDestination = "destination"
)

// Validate validates config field values for a pipeline
func Validate(cfg Pipeline) error {
	var err, tmpErr error
	if cfg.ID == "" {
		err = multierror.Append(err, cerrors.Errorf(`id is mandatory: %w`, ErrMandatoryField))
	}
	if len(cfg.ID) > pipeline.IDLengthLimit {
		err = multierror.Append(err, pipeline.ErrIDOverLimit)
	}
	if len(cfg.Name) > pipeline.NameLengthLimit {
		err = multierror.Append(err, pipeline.ErrNameOverLimit)
	}
	if len(cfg.Description) > pipeline.DescriptionLengthLimit {
		err = multierror.Append(err, pipeline.ErrDescriptionOverLimit)
	}
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		err = multierror.Append(err, cerrors.Errorf(`"status" is invalid: %w`, ErrInvalidField))
	}
	tmpErr = validateConnectors(cfg.Connectors)
	if tmpErr != nil {
		err = multierror.Append(err, tmpErr)
	}
	tmpErr = validateProcessors(cfg.Processors)
	if tmpErr != nil {
		err = multierror.Append(err, tmpErr)
	}
	return err
}

// validateConnectors validates config field values for connectors
func validateConnectors(mp []Connector) error {
	var err, pErr error
	ids := make(map[string]bool)
	for _, cfg := range mp {
		if cfg.ID == "" {
			err = multierror.Append(err, cerrors.Errorf(`id is mandatory: %w`, ErrMandatoryField))
		}
		if len(cfg.ID) > connector.IDLengthLimit {
			err = multierror.Append(err, connector.ErrIDOverLimit)
		}
		if len(cfg.Name) > connector.NameLengthLimit {
			err = multierror.Append(err, connector.ErrNameOverLimit)
		}
		if cfg.Plugin == "" {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"plugin\" is mandatory: %w", cfg.ID, ErrMandatoryField))
		}
		if cfg.Type == "" {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"type\" is mandatory: %w", cfg.ID, ErrMandatoryField))
		}
		if cfg.Type != "" && cfg.Type != TypeSource && cfg.Type != TypeDestination {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"type\" is invalid: %w", cfg.ID, ErrInvalidField))
		}
		pErr = validateProcessors(cfg.Processors)
		if pErr != nil {
			err = multierror.Append(err, cerrors.Errorf("connector %q: %w", cfg.ID, pErr))
		}
		if ids[cfg.ID] {
			err = multierror.Append(err, cerrors.Errorf("connector %q: \"id\" must be unique: %w", cfg.ID, ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return err
}

// validateProcessorsConfig validates config field values for processors
func validateProcessors(mp []Processor) error {
	var err error
	ids := make(map[string]bool)
	for _, cfg := range mp {
		if cfg.Type == "" && cfg.Plugin == "" {
			err = multierror.Append(
				err,
				cerrors.Errorf(
					"processor %q: \"plugin\" needs to be provided: %w",
					cfg.ID,
					ErrMandatoryField,
				),
			)
		}
		if cfg.Workers < 0 {
			err = multierror.Append(err, cerrors.Errorf("processor %q: \"workers\" can't be negative: %w", cfg.ID, ErrInvalidField))
		}
		if ids[cfg.ID] {
			err = multierror.Append(err, cerrors.Errorf("processor %q: \"id\" must be unique: %w", cfg.ID, ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return err
}
