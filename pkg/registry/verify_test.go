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

package registry_test

import (
	"context"
	"os"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// TestFailClosedVerifier_VerifyArtifact_AlwaysRefuses is the structural
// guarantee plan-v2 §2.2 depends on: no build without PR-2's real trust
// core can ever accept an artifact as verified, for any input whatsoever.
func TestFailClosedVerifier_VerifyArtifact_AlwaysRefuses(t *testing.T) {
	is := is.New(t)
	v := registry.FailClosedVerifier{}

	result, err := v.VerifyArtifact(context.Background(), registry.ArtifactRef{}, trust.PinnedIdentity{
		OIDCIssuer:      "https://token.actions.githubusercontent.com",
		IdentityPattern: `^https://github\.com/ConduitIO/conduit-connector-postgres/.*$`,
	})
	is.True(err != nil)
	is.True(cerrors.Is(err, registry.ErrVerificationNotConfigured))
	is.Equal(result, registry.VerifyResult{})

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, registry.CodeVerificationUnavailable)
}

// TestFailClosedVerifier_VerifyIndex_NeverMarksVerified is the second,
// structural belt-and-suspenders check plan-v2 §2.2 calls for: even though
// VerifyIndex successfully parses a well-formed index, it must report
// Verified: false, since no signature was actually checked.
func TestFailClosedVerifier_VerifyIndex_NeverMarksVerified(t *testing.T) {
	is := is.New(t)
	v := registry.FailClosedVerifier{}

	raw, err := os.ReadFile("index/testdata/sample-index.json")
	is.NoErr(err)

	got, err := v.VerifyIndex(context.Background(), raw)
	is.NoErr(err)
	is.True(got != nil)
	is.Equal(got.Verified, false)
	is.Equal(got.Payload.SchemaVersion, 1)
}

func TestFailClosedVerifier_VerifyIndex_PropagatesParseErrors(t *testing.T) {
	is := is.New(t)
	v := registry.FailClosedVerifier{}

	_, err := v.VerifyIndex(context.Background(), []byte(`not json`))
	is.True(err != nil)
}
