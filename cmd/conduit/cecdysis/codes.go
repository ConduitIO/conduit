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

package cecdysis

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeInvalidUsage is raised when a command is invoked wrongly, before it
// does any work: an unknown or malformed flag, a missing or extra positional
// argument, a required flag left out, or flag values that fail to parse. It
// is distinct from common.invalid_argument because the fix is always to
// change the command line (see the command's --help), not the input it
// reads. InvalidArgument puts it in the validation exit bucket (exit 2,
// pkg/conduit/exitcode).
var CodeInvalidUsage = conduiterr.Register("cli.invalid_usage", codes.InvalidArgument)
