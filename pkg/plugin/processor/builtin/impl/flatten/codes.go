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

package flatten

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Stable error codes of the flatten processor. Messages name record paths,
// never values (CLAUDE.md: errors are API).
var (
	// CodeNotStructured is raised when the flattened side holds raw data, so
	// there are no field names to flatten. A nil side is not an error.
	CodeNotStructured = conduiterr.Register("flatten.not_structured", codes.FailedPrecondition)
	// CodeKeyCollision is raised when two paths flatten to the same field
	// name. The record fails instead of one value overwriting the other
	// (invariant 6).
	CodeKeyCollision = conduiterr.Register("flatten.key_collision", codes.InvalidArgument)
)
