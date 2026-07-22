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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Index error codes — the "index"-owned rows of the canonical registry
// error table (plan-v2 §4). Registered here (not in pkg/registry) so
// pkg/registry can import this package without a cycle; pkg/registry and
// pkg/registry/trust reference these directly rather than re-registering
// them under a different reason string.
var (
	// CodeSchemaTooNew is raised when payload.schemaVersion exceeds the
	// highest this build was compiled to understand.
	CodeSchemaTooNew = conduiterr.Register("registry.schema_too_new", codes.FailedPrecondition)
	// CodeIndexUnreachable is raised when fetching the index fails at the
	// network/HTTP layer (distinct from a fetch that succeeds but is too
	// large, stale, or rolled back).
	CodeIndexUnreachable = conduiterr.Register("registry.index_unreachable", codes.Unavailable)
	// CodeIndexTooLarge is raised when a fetched index exceeds the P0-2 size
	// cap (plan-v2 §2.4 item 1). ResourceExhausted already carries a defined
	// pkg/conduit/exitcode bucket (Environment) — see fetch.go's doc comment
	// for the footnote this resolves.
	CodeIndexTooLarge = conduiterr.Register("registry.index_too_large", codes.ResourceExhausted)
	// CodeIndexNestingTooDeep is raised when the duplicate-key walker's
	// recursion cap is hit (P0-2 item 2) — refuse, never stack-overflow.
	CodeIndexNestingTooDeep = conduiterr.Register("registry.index_nesting_too_deep", codes.FailedPrecondition)
	// CodeIndexIntegrity is raised when a recognized keyId's cryptographic
	// verification fails (tampering/corruption) — and, per this package's
	// duplicate-key walker, also when parse-time duplicate-key rejection
	// fires: R-1 frames duplicate-key resolution ambiguity as itself a
	// "signature-bypass primitive" (a producer/verifier parser
	// differential), which is the same integrity concern this code names,
	// not a separate condition. See duplicatekey.go's doc comment.
	CodeIndexIntegrity = conduiterr.Register("registry.index_integrity", codes.DataLoss)
	// CodeTrustAnchorExpired is raised when no keyId in signatures[] matches
	// any of this build's compiled-in trust anchors at all — the "upgrade
	// Conduit" case, distinct from CodeIndexIntegrity's "recognized key,
	// verification failed".
	CodeTrustAnchorExpired = conduiterr.Register("registry.trust_anchor_expired", codes.FailedPrecondition)
	// CodeIndexStale is raised when index.timestamp is older than
	// maxStaleness — distinct from CodeIndexUnreachable and
	// CodeIndexRollback.
	CodeIndexStale = conduiterr.Register("registry.index_stale", codes.FailedPrecondition)
	// CodeIndexRollback is raised when index.version is below the locally
	// persisted high-water mark.
	CodeIndexRollback = conduiterr.Register("registry.index_rollback", codes.FailedPrecondition)
	// CodeVersionYanked is raised when a resolved/pinned version carries
	// `yanked`. Registered here (owning package "index" per plan-v2 §4,
	// which lists it as shared with "registry") rather than in
	// pkg/registry, specifically so this package need not import
	// pkg/registry (which imports this package) — audit's REVOKED_PUBLISHER
	// finding (PR-4) reuses this code verbatim rather than minting a new
	// connector.*-prefixed one.
	CodeVersionYanked = conduiterr.Register("registry.version_yanked", codes.FailedPrecondition)
)
