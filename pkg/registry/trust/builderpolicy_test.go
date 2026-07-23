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

package trust_test

import (
	"regexp"
	"testing"

	"github.com/conduitio/conduit/pkg/registry/trust"
	"github.com/matryer/is"
)

// BuilderPinnedIdentity must match the SLSA generator identity (ExpectedBuilderID)
// that actually signs the provenance — NOT a connector's own identity. This is
// the fix for the identity-mismatch that rejected every genuine L3 provenance:
// the provenance cert SAN is the isolated builder's reusable-workflow ref.
func TestBuilderPinnedIdentity_MatchesExpectedBuilderID(t *testing.T) {
	is := is.New(t)
	id := trust.BuilderPinnedIdentity()

	is.Equal(id.OIDCIssuer, "https://token.actions.githubusercontent.com")

	re, err := regexp.Compile(id.IdentityPattern)
	is.NoErr(err)
	// The pattern must match the exact generator ref (the real provenance cert SAN)…
	is.True(re.MatchString(trust.ExpectedBuilderID))
	// …be fully anchored (defensive pinning tightness)…
	is.True(len(id.IdentityPattern) > 0 && id.IdentityPattern[0] == '^' && id.IdentityPattern[len(id.IdentityPattern)-1] == '$')
	// …and NOT match a connector's own publish-workflow identity (which signs the
	// artifact, not the provenance).
	is.True(!re.MatchString("https://github.com/ConduitIO/conduit-connector-file/.github/workflows/publish.yml@refs/tags/v0.10.7"))
}
