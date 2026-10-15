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
