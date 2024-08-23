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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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
	var errs []error
	if cfg.ID == "" {
		errs = append(errs, cerrors.Errorf(`id is mandatory: %w`, ErrMandatoryField))
	}
	if len(cfg.ID) > pipeline.IDLengthLimit {
		errs = append(errs, pipeline.ErrIDOverLimit)
	}
	if len(cfg.Name) > pipeline.NameLengthLimit {
		errs = append(errs, pipeline.ErrNameOverLimit)
	}
	if len(cfg.Description) > pipeline.DescriptionLengthLimit {
		errs = append(errs, pipeline.ErrDescriptionOverLimit)
	}
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		errs = append(errs, cerrors.Errorf(`"status" is invalid: %w`, ErrInvalidField))
	}

	errs = append(errs, validateConnectors(cfg.Connectors)...)
	errs = append(errs, validateProcessors(cfg.Processors)...)

	return cerrors.Join(errs...)
}

// validateConnectors validates config field values for connectors
func validateConnectors(mp []Connector) []error {
	var errs []error
	ids := make(map[string]bool)
	for _, cfg := range mp {
		if cfg.ID == "" {
			errs = append(errs, cerrors.Errorf(`id is mandatory: %w`, ErrMandatoryField))
		}
		if len(cfg.ID) > connector.IDLengthLimit {
			errs = append(errs, connector.ErrIDOverLimit)
		}
		if len(cfg.Name) > connector.NameLengthLimit {
			errs = append(errs, connector.ErrNameOverLimit)
		}
		if cfg.Plugin == "" {
			errs = append(errs, cerrors.Errorf("connector %q: \"plugin\" is mandatory: %w", cfg.ID, ErrMandatoryField))
		}
		if cfg.Type == "" {
			errs = append(errs, cerrors.Errorf("connector %q: \"type\" is mandatory: %w", cfg.ID, ErrMandatoryField))
		}
		if cfg.Type != "" && cfg.Type != TypeSource && cfg.Type != TypeDestination {
			errs = append(errs, cerrors.Errorf("connector %q: \"type\" is invalid: %w", cfg.ID, ErrInvalidField))
		}

		pErrs := validateProcessors(cfg.Processors)
		for _, pErr := range pErrs {
			errs = append(errs, cerrors.Errorf("connector %q: %w", cfg.ID, pErr))
		}

		if ids[cfg.ID] {
			errs = append(errs, cerrors.Errorf("connector %q: \"id\" must be unique: %w", cfg.ID, ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}

// validateProcessorsConfig validates config field values for processors
func validateProcessors(mp []Processor) []error {
	var errs []error
	ids := make(map[string]bool)
	for _, cfg := range mp {
		if cfg.Plugin == "" {
			errs = append(errs, cerrors.Errorf("processor %q: \"plugin\" is mandatory: %w", cfg.ID, ErrMandatoryField))
		}
		if ids[cfg.ID] {
			errs = append(errs, cerrors.Errorf("processor %q: \"id\" must be unique: %w", cfg.ID, ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}
