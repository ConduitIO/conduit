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

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

// Sentinel errors for the three ways artifact verification can fail (R-1
// §c step 6/7). These are the sentinels the shared adversarial fixture
// corpus (plan-v2 §11, P1-4, shipping in PR-2 alongside the real bodies
// that return them) compares against; the real verification functions in
// PR-2 wrap one of these as the cause of a conduiterr.Wrap(CodeUnsigned/
// CodeIdentityMismatch/CodeProvenanceInvalid, ..., err) so both a plain
// cerrors.Is(err, trust.ErrUnsigned) check and the conduiterr code are
// available to callers.
var (
	// ErrUnsigned means there is no valid signature for the pinned identity
	// at all (R-1 §c step 6): no signature verified, full stop — as opposed
	// to a signature that verified but for the wrong identity
	// (ErrIdentityMismatch).
	ErrUnsigned = cerrors.New("trust: no valid signature for the pinned identity")
	// ErrIdentityMismatch means a signature DID verify (a validly-logged
	// Rekor entry exists), but for a different certificate identity than
	// the one pinned for this connector name.
	ErrIdentityMismatch = cerrors.New("trust: signature valid but certificate identity does not match the pinned identity")
	// ErrProvenanceInvalid means the SLSA provenance attestation failed the
	// subject-digest match (R-1 §c step 7a) or the builder.id/
	// configSource.uri binding to the pinned identity (step 7b) — a
	// validly-signed attestation existed, but it does not actually attest
	// to this artifact having been built by the pinned identity's pipeline.
	ErrProvenanceInvalid = cerrors.New("trust: provenance attestation failed subject-digest or builder binding")
)
