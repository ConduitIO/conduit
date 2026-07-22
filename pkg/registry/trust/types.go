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

// PinnedIdentity is a connector name's per-name identity pin (R-1 §c): the
// OIDC issuer and certificate-identity pattern a valid signing identity
// must present. It mirrors index.Publisher's ExpectedOIDCIssuer/
// ExpectedIdentityPattern fields but is this package's own type rather than
// a re-export of index.Publisher, so pkg/registry.ArtifactVerifier's
// signature does not force every caller to depend on the full index schema
// just to pass an identity pin through.
type PinnedIdentity struct {
	// OIDCIssuer is the OIDC issuer URL a valid signing identity must
	// present, e.g. "https://token.actions.githubusercontent.com" for
	// GitHub Actions keyless signing. Maps to `cosign verify
	// --certificate-oidc-issuer`.
	OIDCIssuer string
	// IdentityPattern is a fully-anchored (^...$) regular expression
	// matching the certificate SAN / workflow identity. Maps to `cosign
	// verify --certificate-identity-regexp`. See ValidateIdentityPattern
	// for the tightness rules a pattern must satisfy.
	IdentityPattern string
}
