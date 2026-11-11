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

package field

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Stable error codes of the field.set processor. Messages name config
// parameters and field paths, never record values (CLAUDE.md: errors are API).
var (
	// CodeSetValueAndFrom is raised at configure time when both "value" and
	// "from" are set; they are mutually exclusive.
	CodeSetValueAndFrom = conduiterr.Register("field.set.value_and_from", codes.InvalidArgument)
	// CodeSetValueRequired is raised at configure time when neither "value"
	// nor "from" is set.
	CodeSetValueRequired = conduiterr.Register("field.set.value_required", codes.InvalidArgument)
	// CodeSetTypeWithFrom is raised at configure time when "type" is set
	// together with "from", which keeps the type of the copied field.
	CodeSetTypeWithFrom = conduiterr.Register("field.set.type_with_from", codes.InvalidArgument)
	// CodeSetFromInvalid is raised at configure time when "from" is not a
	// valid field reference (for example a Go template).
	CodeSetFromInvalid = conduiterr.Register("field.set.from_invalid", codes.InvalidArgument)
	// CodeSetFromMissing is raised for a record that does not have the field
	// "from" names (a field, or a parent on its path, is absent or null). A
	// field that holds null is not missing: it is copied as null.
	CodeSetFromMissing = conduiterr.Register("field.set.from_missing", codes.FailedPrecondition)
	// CodeSetSideNotStructured is raised for a record when a copy or a typed
	// set reads from or writes into a key or payload that holds raw data, or
	// would store anything but structured data or null as a whole key or
	// payload (a string would become raw bytes). It is never coerced
	// (invariant 6).
	CodeSetSideNotStructured = conduiterr.Register("field.set.side_not_structured", codes.FailedPrecondition)
	// CodeSetTypeConversion is raised for a record whose "value" output cannot
	// be converted to "type". The value is never coerced (invariant 6).
	CodeSetTypeConversion = conduiterr.Register("field.set.type_conversion", codes.InvalidArgument)
	// CodeSetValueNotSettable is raised for a record when a typed value or a
	// copied value cannot be stored in "field": a map, an int or null into
	// metadata (null would delete the key, or reset the map), or raw data,
	// the metadata map, the position or the operation into a field inside
	// structured data. It is never coerced (invariant 6).
	CodeSetValueNotSettable = conduiterr.Register("field.set.value_not_settable", codes.FailedPrecondition)
)

// newSetConfigError returns a configure-time coded error for parameter param.
func newSetConfigError(code conduiterr.Code, msg, param, suggestion string) error {
	e := conduiterr.New(code, msg)
	e.ConfigPath = "settings." + param
	e.Suggestion = suggestion
	return e
}

// setV010Parameters are the parameters of field.set before `from` and `type`
// existed. Configure validates a configuration that sets neither of them
// against these, so it fails exactly as v0.1.0 did, with the same error.
// TestSetField_ConfigureIsUnchanged compares the two.
func setV010Parameters() config.Parameters {
	return config.Parameters{
		setConfigField: {
			Type: config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationExclusion{List: []string{".Position"}},
			},
		},
		setConfigValue: {
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{config.ValidationRequired{}},
		},
	}
}
