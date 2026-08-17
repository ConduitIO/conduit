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

package generate

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Error codes owned by the generation loop, from the generate design doc's §8
// taxonomy. They are a public contract — agents and scripts branch on the
// reason string — so they are never reworded or renumbered without a
// deprecation note.
//
// Provider-resolution and provider-call codes live in the provider
// subpackage; these are the two the loop itself raises when a model's output
// never clears a gate within the retry budget.
var (
	// CodeParseFailed: the provider replied, but nothing in the reply could
	// be read as a pipeline config within the retry budget. Internal (exit 1,
	// runtime) rather than InvalidArgument, because the user's request was
	// well-formed — the boundary failed, not their input.
	CodeParseFailed = conduiterr.Register("generate.parse_failed", codes.Internal)

	// CodeValidateFailed: a candidate parsed but never passed validate within
	// the retry budget. FailedPrecondition (exit 2, validation): the request
	// was understood, the artifact it produced is not usable as-is, and the
	// findings say exactly why.
	//
	// The last attempt's validate.Report travels back on Result.Report — it is
	// never discarded, because the findings are the only thing that tells the
	// user (or an agent) what to change.
	CodeValidateFailed = conduiterr.Register("generate.validate_failed", codes.FailedPrecondition)
)
