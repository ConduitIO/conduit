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

package index

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// Canonicalize returns the RFC 8785 (JCS) canonical byte form of a JSON
// value: object keys sorted (by UTF-16 code unit), numbers in a fixed
// representation, minimal escaping, no insignificant whitespace.
//
// This is the exact byte sequence a signature is computed over and verified
// against (R-1 §a) — both the signing side (index build tooling) and every
// verifying side (this package's future Verify, index-CI's independent
// re-verification) must run the identical implementation, per R-1's OQ5
// resolution ("whatever is chosen must be the SAME implementation ... on
// both the index-build side and index-CI's re-verification side"). That is
// why this is a single, shared, well-tested wrapper rather than an inline
// call at each site — a canonicalization mismatch between producer and
// verifier would reintroduce exactly the class of bug JCS exists to avoid.
//
// Canonicalize has no cryptographic dependency and makes no trust decision
// of its own: it is a pure byte transform, safe to run before any signature
// has been checked (and, indeed, is a required INPUT to checking one).
func Canonicalize(payload []byte) ([]byte, error) {
	out, err := jsoncanonicalizer.Transform(payload)
	if err != nil {
		return nil, cerrors.Errorf("could not canonicalize JSON payload: %w", err)
	}
	return out, nil
}
