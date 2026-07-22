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

package trust

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Trust error codes — the "trust"-owned rows of the canonical registry
// error table (plan-v2 §4). None of these are triggered by any PR-0 code
// path yet (the real verification bodies land in PR-2); they are
// registered now per plan-v2 §2.3 so the full table exists from the first
// PR and docs/llms.txt generation has a stable, complete source from day
// one.
var (
	// CodeBundleTooLarge is raised when a signature or provenance bundle
	// fetch exceeds the P0-2 size cap. ResourceExhausted already carries a
	// defined pkg/conduit/exitcode bucket (Environment) — see
	// index.CodeIndexTooLarge's doc comment for the same confirmation.
	CodeBundleTooLarge = conduiterr.Register("registry.bundle_too_large", codes.ResourceExhausted)
	// CodeIdentityRevoked is raised when a connector's publisher carries
	// `revoked` — every version under that name is refused regardless of
	// individual yanked status. audit's REVOKED_PUBLISHER finding (PR-4)
	// reuses this code verbatim.
	CodeIdentityRevoked = conduiterr.Register("registry.identity_revoked", codes.PermissionDenied)
	// CodeUnsigned is raised when there is no valid signature for the
	// pinned identity at all (trust.ErrUnsigned).
	CodeUnsigned = conduiterr.Register("registry.unsigned", codes.PermissionDenied)
	// CodeIdentityMismatch is raised when a signature verifies but for a
	// different certificate identity than the one pinned
	// (trust.ErrIdentityMismatch).
	CodeIdentityMismatch = conduiterr.Register("registry.identity_mismatch", codes.PermissionDenied)
	// CodeProvenanceInvalid is raised when the SLSA provenance's
	// subject-digest match or builder.id/configSource.uri binding fails
	// (trust.ErrProvenanceInvalid).
	CodeProvenanceInvalid = conduiterr.Register("registry.provenance_invalid", codes.PermissionDenied)
)
