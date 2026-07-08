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
