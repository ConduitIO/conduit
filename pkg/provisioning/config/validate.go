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
// ConduitError code, a JSON-pointer configPath to the offending field, and a
// Suggestion for how to fix it; the original sentinel stays in the chain for
// backward-compatible errors.Is checks.
func Validate(cfg Pipeline) error {
	var errs []error
	if cfg.ID == "" {
		errs = append(errs, fieldError(CodeFieldRequired, "/id", "id is mandatory",
			`set "id" to a unique pipeline identifier`, ErrMandatoryField))
	}
	if len(cfg.ID) > pipeline.IDLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/id", pipeline.ErrIDOverLimit.Error(),
			fmt.Sprintf("shorten \"id\" to at most %d characters", pipeline.IDLengthLimit), pipeline.ErrIDOverLimit))
	}
	if len(cfg.Name) > pipeline.NameLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/name", pipeline.ErrNameOverLimit.Error(),
			fmt.Sprintf("shorten \"name\" to at most %d characters", pipeline.NameLengthLimit), pipeline.ErrNameOverLimit))
	}
	if len(cfg.Description) > pipeline.DescriptionLengthLimit {
		errs = append(errs, fieldError(CodeFieldTooLong, "/description", pipeline.ErrDescriptionOverLimit.Error(),
			fmt.Sprintf("shorten \"description\" to at most %d characters", pipeline.DescriptionLengthLimit), pipeline.ErrDescriptionOverLimit))
	}
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		errs = append(errs, fieldError(CodeFieldInvalid, "/status", `"status" is invalid`,
			fmt.Sprintf("set \"status\" to %q or %q", StatusRunning, StatusStopped), ErrInvalidField))
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
			errs = append(errs, fieldError(CodeFieldRequired, path+"/id", fmt.Sprintf("connector %d: id is mandatory", i+1),
				fmt.Sprintf("set connectors[%d].id", i), ErrMandatoryField))
		}
		if len(cfg.ID) > connector.IDLengthLimit {
			errs = append(errs, fieldError(CodeFieldTooLong, path+"/id", fmt.Sprintf("connector %q: %s", cfg.ID, connector.ErrIDOverLimit),
				fmt.Sprintf("shorten connectors[%d].id to at most %d characters", i, connector.IDLengthLimit), connector.ErrIDOverLimit))
		}
		if len(cfg.Name) > connector.NameLengthLimit {
			errs = append(errs, fieldError(CodeFieldTooLong, path+"/name", fmt.Sprintf("connector %q: %s", cfg.ID, connector.ErrNameOverLimit),
				fmt.Sprintf("shorten connectors[%d].name to at most %d characters", i, connector.NameLengthLimit), connector.ErrNameOverLimit))
		}
		if cfg.Plugin == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/plugin", fmt.Sprintf(`connector %q: "plugin" is mandatory`, cfg.ID),
				fmt.Sprintf("set connectors[%d].plugin (e.g. \"builtin:postgres\")", i), ErrMandatoryField))
		}
		if cfg.Type == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/type", fmt.Sprintf(`connector %q: "type" is mandatory`, cfg.ID),
				fmt.Sprintf("set connectors[%d].type to %q or %q", i, TypeSource, TypeDestination), ErrMandatoryField))
		}
		if cfg.Type != "" && cfg.Type != TypeSource && cfg.Type != TypeDestination {
			errs = append(errs, fieldError(CodeFieldInvalid, path+"/type", fmt.Sprintf(`connector %q: "type" is invalid`, cfg.ID),
				fmt.Sprintf("set connectors[%d].type to %q or %q", i, TypeSource, TypeDestination), ErrInvalidField))
		}

		// nested processor errors already carry their own /connectors/i/processors/j path
		errs = append(errs, validateProcessors(cfg.Processors, path+"/processors")...)

		if ids[cfg.ID] {
			errs = append(errs, fieldError(CodeIDDuplicate, path+"/id", fmt.Sprintf(`connector %q: "id" must be unique`, cfg.ID),
				fmt.Sprintf("rename one of the connectors sharing id %q", cfg.ID), ErrDuplicateID))
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
			errs = append(errs, fieldError(CodeFieldRequired, path+"/id", fmt.Sprintf("processor %d: id is mandatory", i+1),
				fmt.Sprintf("set %s/%d.id", pathPrefix, i), ErrMandatoryField))
		}
		if cfg.Plugin == "" {
			errs = append(errs, fieldError(CodeFieldRequired, path+"/plugin", fmt.Sprintf(`processor %q: "plugin" is mandatory`, cfg.ID),
				fmt.Sprintf("set %s/%d.plugin (e.g. \"js\")", pathPrefix, i), ErrMandatoryField))
		}
		if cfg.Workers < 0 {
			errs = append(errs, fieldError(CodeFieldInvalid, path+"/workers", fmt.Sprintf(`processor %q: "workers" can't be negative`, cfg.ID),
				fmt.Sprintf("set %s/%d.workers to zero or a positive number", pathPrefix, i), ErrInvalidField))
		}
		if ids[cfg.ID] {
			errs = append(errs, fieldError(CodeIDDuplicate, path+"/id", fmt.Sprintf(`processor %q: "id" must be unique`, cfg.ID),
				fmt.Sprintf("rename one of the processors sharing id %q", cfg.ID), ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}
