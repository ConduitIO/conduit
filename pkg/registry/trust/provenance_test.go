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
	"crypto/sha256"
	"encoding/hex"
	"testing"

	in_toto "github.com/in-toto/attestation/go/v1"
	"github.com/matryer/is"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("could not build structpb.Struct: %v", err)
	}
	return s
}

// TestCheckProvenanceBinding_UnrecognizedPredicateTypeIsHardReject proves a
// predicateType this build doesn't recognize is a HARD REJECT, never a
// silently-skipped check — matching the frozen index schema's
// provenanceRef.predicateType doc comment ("a predicate type the client
// does not recognize is a hard reject").
func TestCheckProvenanceBinding_UnrecognizedPredicateTypeIsHardReject(t *testing.T) {
	is := is.New(t)
	digest := sha256.Sum256([]byte("artifact"))
	stmt := &in_toto.Statement{
		PredicateType: "https://example.com/some-future-unknown-predicate",
		Subject: []*in_toto.ResourceDescriptor{
			{Name: "x", Digest: map[string]string{"sha256": hex.EncodeToString(digest[:])}},
		},
		Predicate: mustStruct(t, map[string]any{}),
	}
	err := trust.CheckProvenanceBinding(stmt, digest, trust.ExpectedBuilderID)
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrProvenanceInvalid))
}

// TestCheckProvenanceBinding_SHA256OnlyDigestRequired proves the P2
// subject-digest-algorithm-pinning nit: a subject digest map that offers
// some OTHER algorithm (even a real, matching one) but no "sha256" key at
// all never counts as a match.
func TestCheckProvenanceBinding_SHA256OnlyDigestRequired(t *testing.T) {
	is := is.New(t)
	digest := sha256.Sum256([]byte("artifact"))
	stmt := &in_toto.Statement{
		PredicateType: "https://slsa.dev/provenance/v1",
		Subject: []*in_toto.ResourceDescriptor{
			{Name: "x", Digest: map[string]string{"sha512": "irrelevant-legacy-digest-present-but-no-sha256"}},
		},
		Predicate: mustStruct(t, map[string]any{
			"runDetails": map[string]any{"builder": map[string]any{"id": trust.ExpectedBuilderID}},
		}),
	}
	err := trust.CheckProvenanceBinding(stmt, digest, trust.ExpectedBuilderID)
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrProvenanceInvalid))
}

// TestCheckProvenanceBinding_NilStatementRefuses guards against a caller
// passing a nil statement through (e.g. a future refactor of the
// verification pipeline) ever being silently treated as "nothing to check,
// so allow".
func TestCheckProvenanceBinding_NilStatementRefuses(t *testing.T) {
	is := is.New(t)
	digest := sha256.Sum256([]byte("artifact"))
	err := trust.CheckProvenanceBinding(nil, digest, trust.ExpectedBuilderID)
	is.True(err != nil)
	is.True(cerrors.Is(err, trust.ErrProvenanceInvalid))
}

// TestCheckProvenanceBinding_V02FlatBuilderShape proves the OLDER SLSA v0.2
// predicate shape (flat predicate.builder.id, no runDetails nesting) is
// still supported — CheckProvenanceBinding dispatches on predicateType
// rather than assuming every recognized type shares one shape.
func TestCheckProvenanceBinding_V02FlatBuilderShape(t *testing.T) {
	is := is.New(t)
	digest := sha256.Sum256([]byte("artifact"))
	stmt := &in_toto.Statement{
		PredicateType: "https://slsa.dev/provenance/v0.2",
		Subject: []*in_toto.ResourceDescriptor{
			{Name: "x", Digest: map[string]string{"sha256": hex.EncodeToString(digest[:])}},
		},
		Predicate: mustStruct(t, map[string]any{
			"builder": map[string]any{"id": trust.ExpectedBuilderID},
		}),
	}
	err := trust.CheckProvenanceBinding(stmt, digest, trust.ExpectedBuilderID)
	is.NoErr(err)
}
