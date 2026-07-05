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
	"fmt"

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

// Validate validates config field values for a pipeline. Each error carries a
// ConduitError code and a JSON-pointer configPath to the offending field; the
// original sentinel stays in the chain for backward-compatible errors.Is checks.
func Validate(cfg Pipeline) error {
	var errs []error
	if cfg.ID == "" {
		errs = append(errs, fieldError(CodeFieldRequired, "/id", "id is mandatory", ErrMandatoryField))
	}
	if len(cfg.ID) > pipeline.IDLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/id", pipeline.ErrIDOverLimit.Error(), pipeline.ErrIDOverLimit))
	}
	if len(cfg.Name) > pipeline.NameLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/name", pipeline.ErrNameOverLimit.Error(), pipeline.ErrNameOverLimit))
	}
	if len(cfg.Description) > pipeline.DescriptionLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/description", pipeline.ErrDescriptionOverLimit.Error(), pipeline.ErrDescriptionOverLimit))
	}
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		errs = append(errs, fieldError(CodeFieldInvalid, "/status", `"status" is invalid`, ErrInvalidField))
	}

	errs = append(errs, validateConnectors(cfg.Connectors)...)
	errs = append(errs, validateProcessors(cfg.Processors, "/processors")...)

	return cerrors.Join(errs...)
}

// validateConnectors validates config field values for connectors. pathPrefix is
// the JSON-pointer to the connectors slice ("/connectors").
func validateConnectors(mp []Connector) []error {
	var errs []error
	ids := make(map[string]bool)
	for i, cfg := range mp {
		path := fmt.Sprintf("/connectors/%d", i)
		if cfg.ID == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/id", fmt.Sprintf("connector %d: id is mandatory", i+1), ErrMandatoryField))
		}
		if len(cfg.ID) > connector.IDLengthLimit {
			errs = append(errs, fieldError(CodeFieldTooLong, path+"/id", fmt.Sprintf("connector %q: %s", cfg.ID, connector.ErrIDOverLimit), connector.ErrIDOverLimit))
		}
		if len(cfg.Name) > connector.NameLengthLimit {
			errs = append(errs, fieldError(CodeFieldTooLong, path+"/name", fmt.Sprintf("connector %q: %s", cfg.ID, connector.ErrNameOverLimit), connector.ErrNameOverLimit))
		}
		if cfg.Plugin == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/plugin", fmt.Sprintf(`connector %q: "plugin" is mandatory`, cfg.ID), ErrMandatoryField))
		}
		if cfg.Type == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/type", fmt.Sprintf(`connector %q: "type" is mandatory`, cfg.ID), ErrMandatoryField))
		}
		if cfg.Type != "" && cfg.Type != TypeSource && cfg.Type != TypeDestination {
			errs = append(errs, fieldError(CodeFieldInvalid, path+"/type", fmt.Sprintf(`connector %q: "type" is invalid`, cfg.ID), ErrInvalidField))
		}

		// nested processor errors already carry their own /connectors/i/processors/j path
		errs = append(errs, validateProcessors(cfg.Processors, path+"/processors")...)

		if ids[cfg.ID] {
			errs = append(errs, fieldError(CodeIDDuplicate, path+"/id", fmt.Sprintf(`connector %q: "id" must be unique`, cfg.ID), ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}

// validateProcessors validates config field values for processors. pathPrefix is
// the JSON-pointer to the processors slice (e.g. "/processors" or
// "/connectors/0/processors").
func validateProcessors(mp []Processor, pathPrefix string) []error {
	var errs []error
	ids := make(map[string]bool)
	for i, cfg := range mp {
		path := fmt.Sprintf("%s/%d", pathPrefix, i)
		if cfg.ID == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/id", fmt.Sprintf("processor %d: id is mandatory", i+1), ErrMandatoryField))
		}
		if cfg.Plugin == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/plugin", fmt.Sprintf(`processor %q: "plugin" is mandatory`, cfg.ID), ErrMandatoryField))
		}
		if cfg.Workers < 0 {
			errs = append(errs, fieldError(CodeFieldInvalid, path+"/workers", fmt.Sprintf(`processor %q: "workers" can't be negative`, cfg.ID), ErrInvalidField))
		}
		if ids[cfg.ID] {
			errs = append(errs, fieldError(CodeIDDuplicate, path+"/id", fmt.Sprintf(`processor %q: "id" must be unique`, cfg.ID), ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}
