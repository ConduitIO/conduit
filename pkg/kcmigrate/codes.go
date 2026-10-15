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

package kcmigrate

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Input errors. All three are bad input (exit code 2): the fix is on the
// caller's side. They are public contracts from first release.
var (
	// CodeInputUnreadable is raised when an input cannot be parsed in the
	// format it was detected or declared as: a properties syntax error, a
	// JSON syntax error, or JSON that is not valid UTF-8. ConfigPath carries
	// the location.
	CodeInputUnreadable = conduiterr.Register("migrate.input_unreadable", codes.InvalidArgument)
	// CodeInputUnrecognized is raised when an input parses but is not a shape
	// this package reads: format detection failed, a JSON config value is not
	// a scalar, or the input is a MirrorMaker dedicated-mode mm2.properties.
	CodeInputUnrecognized = conduiterr.Register("migrate.input_unrecognized", codes.InvalidArgument)
	// CodeInputTooLarge is raised when the inputs exceed [MaxInputBytes] in
	// total, [MaxInputs] in number, or [MaxJSONDepth] in JSON nesting.
	CodeInputTooLarge = conduiterr.Register("migrate.input_too_large", codes.InvalidArgument)
)

// Finding codes: the code of a report entry. They are domain results, not
// command errors, so they never appear in the envelope's error field; the
// command reduces a run with blocking findings to exit code 2, which is why
// they are registered as InvalidArgument. They are public contracts from
// first release. Each one's fix text comes from the rule or the engine that
// emits it.
var (
	// CodeKeyUnknown: no rule claims the key. Always blocking: an unknown key
	// may change behavior, and the safe direction is to stop.
	CodeKeyUnknown = conduiterr.Register("kc.key_unknown", codes.InvalidArgument)
	// CodeKeyUnsupported: a known key with no Conduit equivalent for its value.
	CodeKeyUnsupported = conduiterr.Register("kc.key_unsupported", codes.InvalidArgument)
	// CodeKeyApproximated: translated with a documented difference.
	CodeKeyApproximated = conduiterr.Register("kc.key_approximated", codes.InvalidArgument)
	// CodeKeyDeprecated: a legacy key name, handled as its current name.
	CodeKeyDeprecated = conduiterr.Register("kc.key_deprecated", codes.InvalidArgument)
	// CodeKeyDuplicate: an earlier occurrence of a key that appears again
	// later in the same connector config; the last value wins.
	CodeKeyDuplicate = conduiterr.Register("kc.key_duplicate", codes.InvalidArgument)
	// CodeConnectorClassUnsupported: the connector class has no mapping; every
	// key of the connector is listed with this code and no pipeline is
	// written.
	CodeConnectorClassUnsupported = conduiterr.Register("kc.connector_class_unsupported", codes.InvalidArgument)
	// CodeKeyConflict: two keys map onto one Conduit setting. Blocking, since
	// either value could be the one Kafka Connect used.
	CodeKeyConflict = conduiterr.Register("kc.key_conflict", codes.InvalidArgument)
	// CodeSecretPlaintext: a connector-level note that the input holds a
	// secret as a literal value. The value is not copied anywhere, but the
	// input file is itself a leak.
	CodeSecretPlaintext = conduiterr.Register("kc.secret_plaintext", codes.InvalidArgument)
)
