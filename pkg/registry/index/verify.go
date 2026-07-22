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
	"fmt"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// VerifiedIndex wraps a parsed Payload together with an explicit Verified
// flag, so a caller can never mistake a ParseUnverified result (schema-valid
// bytes, cryptographically unchecked) for a trusted one by accident.
// pkg/registry.FailClosedVerifier always sets Verified: false; the eventual
// install pipeline asserts Verified == true before using any field for a
// security decision — a second, structural belt-and-suspenders check on top
// of pkg/registry.ArtifactVerifier unconditionally refusing until PR-2
// lands (plan-v2 §2.2).
type VerifiedIndex struct {
	Payload  Payload
	Verified bool
}

// ParseUnverified parses raw index bytes into a typed Payload WITHOUT
// checking any signature — it is a shape/schema check only, in bold: it
// makes NO claim about the signatures envelope, and its result must never
// be used for a security decision. Concretely, in the order R-1 §a
// requires:
//
//  1. Reject any duplicate JSON key at any nesting level (CheckNoDuplicateKeys,
//     P0-2) — before and independent of interpreting any field.
//  2. Extract payload/signatures generically (schemaVersion is not yet
//     trusted at this point, so nothing schema-version-specific has run).
//  3. Peek payload.schemaVersion and refuse (CodeSchemaTooNew) anything
//     newer than MaxSupportedSchemaVersion, "upgrade Conduit" — before
//     attempting the typed unmarshal, so an unrecognized future shape never
//     reaches struct decoding.
//  4. Unmarshal payload into the typed schemaVersion-1 Payload struct.
//
// The real, cryptographically-verifying counterpart (Verify, checking
// signatures against build-time-fixed TrustAnchors per R-1 §a step 2c)
// lands in PR-2 — nothing in this build may treat ParseUnverified's result
// as trusted for any security decision; see pkg/registry.FailClosedVerifier.
//
// ParseUnverified recovers from a panic anywhere in this pipeline and
// reports it as CodeIndexIntegrity rather than crashing the process — the
// same defense-in-depth as CheckNoDuplicateKeys (see that function's doc
// comment for the concrete goccy/go-json panic the P0-2 fuzz corpus found),
// and for the same reason: this function's whole job is to survive
// attacker-controlled bytes before any crypto check exists to reject them
// outright.
func ParseUnverified(raw []byte) (payload *Payload, err error) {
	defer func() {
		if r := recover(); r != nil {
			payload = nil
			err = conduiterr.New(CodeIndexIntegrity,
				fmt.Sprintf("index payload could not be parsed safely: %v", r))
		}
	}()

	if err := CheckNoDuplicateKeys(raw); err != nil {
		return nil, err
	}

	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index envelope is not valid JSON", err)
	}

	// Peek schemaVersion before the full typed unmarshal, so an
	// unrecognized future schema fails with CodeSchemaTooNew instead of a
	// generic/misleading unmarshal error (R-1 §a: "only succeeds/fails
	// based on cryptography" for the signature step, but schema selection
	// itself must still fail closed and distinctly here).
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(env.Payload, &probe); err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index payload is not valid JSON", err)
	}
	if probe.SchemaVersion > MaxSupportedSchemaVersion {
		return nil, conduiterr.New(CodeSchemaTooNew,
			"index schemaVersion is newer than this build supports — upgrade Conduit")
	}

	var p Payload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, conduiterr.Wrap(CodeIndexIntegrity, "index payload does not match the expected schema", err)
	}
	return &p, nil
}
