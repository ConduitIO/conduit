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

package provider

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Error codes owned by provider resolution, from the generate design doc's §8
// taxonomy. Registering them here (rather than declaring bare strings) is what
// puts them in the error-code reference, llms.txt, and the UI — the registry is
// the single source of truth.
//
// These are a public contract: agents and scripts branch on the reason string,
// so it is never reworded or renumbered without a deprecation note.
var (
	// CodeNoProviderConfigured: zero resolvable candidates. Unauthenticated
	// (exit 3, environment) rather than InvalidArgument, because the user's
	// command was fine — their environment is not.
	CodeNoProviderConfigured = conduiterr.Register("generate.no_provider_configured", codes.Unauthenticated)

	// CodeAmbiguousProvider: more than one candidate and no explicit choice.
	// This is an error rather than a pick — see the package doc.
	CodeAmbiguousProvider = conduiterr.Register("generate.ambiguous_provider_configuration", codes.InvalidArgument)
)

// codeUnknownProvider is the code used when an explicit selection names a
// provider that does not exist.
//
// It is deliberately the FOUNDATIONAL common.invalid_argument rather than a new
// `generate.unknown_provider`: the design doc's §8 error taxonomy is a frozen
// contract and does not list a code for this case. Inventing an unlisted
// `generate.*` code here would add to a public contract outside the document
// that defines it. Flagged for a maintainer decision — if a dedicated code is
// wanted, it belongs in §8 first, then here.
var codeUnknownProvider = conduiterr.CodeInvalidArgument
