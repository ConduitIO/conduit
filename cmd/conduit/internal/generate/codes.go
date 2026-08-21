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

	// CodeSemanticMismatch: the candidate passed validate but did not do what
	// was asked — wrong connector, swapped direction, a requested filter
	// missing. A schema-valid pipeline that confidently does the wrong thing
	// is the failure mode the semantic checker exists for, so it gets its own
	// code rather than hiding inside validate_failed.
	CodeSemanticMismatch = conduiterr.Register("generate.semantic_mismatch", codes.FailedPrecondition)

	// CodeAmbiguousPrompt: the request could not be pinned to a pipeline.
	//
	// Raised in two places, deliberately conservative pre-call (design §9):
	// before any provider call ONLY when the prompt is empty or names one role
	// for two different connectors; otherwise post-hoc, when a candidate's
	// connectors cannot be traced to anything the request actually said. A
	// merely terse request is never refused pre-call — it goes to the model,
	// which reads intent far better than a keyword table, and the result is
	// judged the normal way.
	CodeAmbiguousPrompt = conduiterr.Register("generate.ambiguous_prompt", codes.InvalidArgument)

	// CodeDestinationExists: the resolved output path already exists and
	// --force was not passed (design §8, amended 2026-08-21).
	//
	// It is a code of its own rather than the foundational
	// common.invalid_argument because the consumer is frequently an agent, and
	// "that file already exists" is fixed by re-running with --force or a
	// different --out, while a generic invalid-argument is fixed by changing
	// the request. A caller that cannot tell those apart from the code has to
	// parse the message.
	//
	// AlreadyExists shares the Validation exit bucket with InvalidArgument
	// (pkg/conduit/exitcode, fromGRPCCode), so this is additive: the process
	// still exits 2, only the reason string a machine reads gets more
	// specific.
	CodeDestinationExists = conduiterr.Register("generate.destination_exists", codes.AlreadyExists)
)
