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
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
)

const (
	StatusRunning   = "running"
	StatusStopped   = "stopped"
	TypeSource      = "source"
	TypeDestination = "destination"
)

// fixOpSet is conduiterr.Fix.Op's "set" value — the only op this package's
// v1 repair fix producers ever emit (§6 items #2-#4; item #1's rename is
// produced by the yaml package's linter, not here).
const fixOpSet = "set"

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
		// repair v1 starter set item #4 (design doc §6): truncation is lossy
		// but deterministic and non-data-path, so it is a machine-appliable
		// fix — always shown in the repair diff before apply, never silent
		// (design doc's failure mode 6).
		errs = append(errs, fieldErrorWithFix(CodeFieldTooLong, "/description", pipeline.ErrDescriptionOverLimit.Error(),
			fmt.Sprintf("shorten \"description\" to at most %d characters", pipeline.DescriptionLengthLimit), pipeline.ErrDescriptionOverLimit,
			conduiterr.Fix{Op: fixOpSet, Value: cfg.Description[:pipeline.DescriptionLengthLimit]}))
	}
	if cfg.Status != StatusRunning && cfg.Status != StatusStopped {
		errs = append(errs, statusFieldError(cfg.Status))
	}

	errs = append(errs, validateConnectors(cfg.Connectors)...)
	errs = append(errs, validateProcessors(cfg.Processors, "/processors")...)

	return cerrors.Join(errs...)
}

// statusFieldError builds the /status config.field_invalid error. repair v1
// starter set item #2 (design doc §6): the fix is offered only when the
// invalid value unambiguously maps to a canonical enum member after
// case/whitespace normalization (e.g. " Running " -> "running") — any other
// value is ambiguous (there is no deterministic canonical target), so no Fix
// is attached and repair reports "no machine-appliable fix for this
// finding", exactly as the design doc requires.
func statusFieldError(status string) error {
	msg := `"status" is invalid`
	suggestion := fmt.Sprintf("set \"status\" to %q or %q", StatusRunning, StatusStopped)

	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == StatusRunning || normalized == StatusStopped {
		return fieldErrorWithFix(CodeFieldInvalid, "/status", msg, suggestion, ErrInvalidField,
			conduiterr.Fix{Op: fixOpSet, Value: normalized})
	}
	return fieldError(CodeFieldInvalid, "/status", msg, suggestion, ErrInvalidField)
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
			// repair v1 starter set item #3 (design doc §6): 1 is the
			// ordering-preserving default (workers>1 can reorder records
			// within a key, invariant 4) — the safe direction to fix
			// toward. Classified `restart` by the repair engine's
			// classifier (not `safe`), since changing a processor's worker
			// count would be an EffectRestart change if deployed to a
			// running pipeline.
			errs = append(errs, fieldErrorWithFix(CodeFieldInvalid, path+"/workers", fmt.Sprintf(`processor %q: "workers" can't be negative`, cfg.ID),
				fmt.Sprintf("set %s/%d.workers to zero or a positive number", pathPrefix, i), ErrInvalidField,
				conduiterr.Fix{Op: fixOpSet, Value: "1"}))
		}
		if ids[cfg.ID] {
			errs = append(errs, fieldError(CodeIDDuplicate, path+"/id", fmt.Sprintf(`processor %q: "id" must be unique`, cfg.ID),
				fmt.Sprintf("rename one of the processors sharing id %q", cfg.ID), ErrDuplicateID))
		}
		ids[cfg.ID] = true
	}
	return errs
}
