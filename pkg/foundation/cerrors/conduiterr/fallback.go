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

package conduiterr

import "google.golang.org/grpc/codes"

// WithUnknownReason builds a ConduitError carrying the CodeUnknown reason
// ("internal.unknown") but category as its gRPC status category, rather than
// CodeUnknown's own (codes.Internal).
//
// It exists for API boundaries migrating sentinel-by-sentinel to registered
// Codes (execution plan §1.1, the CI error-code guard): a boundary that has
// not yet registered a Code for some error class may already have determined
// the right gRPC category from legacy cerrors.Is matching, and should keep
// returning that category unchanged — only the *reason* is unknown, not the
// category. WithCode(err, CodeUnknown) would be wrong here: it would also
// downgrade the category to codes.Internal, silently coarsening a NotFound or
// InvalidArgument into an Internal error for every not-yet-migrated sentinel,
// which is not additive.
//
// Once the corresponding sentinel gets its own registered Code, callers
// should switch to that Code (via Wrap, at the origination site) instead of
// this fallback — see the package doc's migration note.
func WithUnknownReason(err error, category codes.Code) *ConduitError {
	e := WithCode(err, CodeUnknown)
	e.Code = Code{reason: CodeUnknown.reason, grpcCode: category}
	return e
}
