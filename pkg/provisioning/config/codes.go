// Copyright © 2026 Meroxa, Inc.
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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Pipeline-config validation error codes. Every validation error carries one of
// these codes plus a JSON-pointer configPath to the offending field, so an API,
// MCP, or UI consumer knows exactly what to fix and where.
var (
	CodeFieldRequired = conduiterr.Register("config.field_required", codes.InvalidArgument)
	CodeFieldInvalid  = conduiterr.Register("config.field_invalid", codes.InvalidArgument)
	CodeFieldTooLong  = conduiterr.Register("config.field_too_long", codes.InvalidArgument)
	CodeIDDuplicate   = conduiterr.Register("config.id_duplicate", codes.InvalidArgument)
	// CodeFieldRenamed is the stable code carried by a lint warning for a
	// deprecated field that was mechanically renamed (the old key's value
	// moves unchanged to a new key) — see
	// pkg/provisioning/config/yaml/internal.Change.RenamedTo and
	// (*configLinter).newWarning. It is distinct from the generic
	// validate.CodeLintWarning so cmd/conduit/internal/repair's fix-producer
	// scan (design doc 20260712-repair-command.md §6, item #1) can find
	// exactly these findings without pattern-matching warning text.
	CodeFieldRenamed = conduiterr.Register("config.field_renamed", codes.InvalidArgument)
	// CodeParseError is raised when a pipeline config document can't be parsed
	// at all (invalid YAML, unrecognized version). It exists for offline
	// consumers (cmd/conduit/internal/validate) that need a stable code for
	// parser failures, which the parser itself returns as plain errors since
	// it has no per-field configPath to attach at that stage.
	CodeParseError = conduiterr.Register("config.parse_error", codes.InvalidArgument)
)

// fieldError builds a ConduitError carrying a machine-actionable code, the
// JSON-pointer path to the offending field, and a human-readable suggestion for
// how to fix it. The sentinel is kept in the chain so existing errors.Is checks
// (ErrMandatoryField, ErrInvalidField, …) still hold.
func fieldError(code conduiterr.Code, configPath, msg, suggestion string, sentinel error) error {
	e := conduiterr.Wrap(code, msg, sentinel)
	e.ConfigPath = configPath
	e.Suggestion = suggestion
	return e
}

// fieldErrorWithFix is fieldError plus a structured, machine-appliable
// conduiterr.Fix — the repair v1 starter set's producer sites (design doc
// 20260712-repair-command.md §6, items #2-#4: /status, negative
// processor/workers, over-long /description) use this instead of fieldError
// so cmd/conduit/internal/repair can offer the fix without any additional
// per-site wiring. fix.ConfigPath is always set to configPath by this
// helper (callers pass a zero-value fix.ConfigPath) so the two can never
// drift.
func fieldErrorWithFix(code conduiterr.Code, configPath, msg, suggestion string, sentinel error, fix conduiterr.Fix) error {
	e := conduiterr.Wrap(code, msg, sentinel)
	e.ConfigPath = configPath
	e.Suggestion = suggestion
	fix.ConfigPath = configPath
	e.Fix = &fix
	return e
}
