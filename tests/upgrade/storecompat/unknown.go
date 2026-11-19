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

package storecompat

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// knownStorePrefixes are the key prefixes the stores this package reads
// claim: pipeline.Store, connector.Store and processor.Store each namespace
// their keys (their storeKeyPrefix constants). Every other prefix in the
// database belongs to nobody in this build.
var knownStorePrefixes = []string{
	"pipeline:instance:",
	"connector:instance:",
	"processor:instance:",
}

// UnknownKeys are store entries under prefixes no current build claims:
// what a database looks like to a binary that predates a newly added
// persisted key. The planned orphan ledger (connectors whose plugin delete
// hook did not succeed) is the concrete case this stands in for, but the
// property under test is prefix-shaped, not ledger-shaped, so these keys
// are deliberately not the ledger's: a key under a brand-new top-level
// namespace, and a key that is a sibling of an existing namespace (the
// risky shape - a store whose prefix match was loosened to "connector:"
// would pick it up and fail to decode it).
//
// The values are not valid encodings of anything these stores decode, and
// they carry a NUL and non-UTF-8 bytes, so a store that read them would
// error rather than silently accept them, and a layer that round-tripped
// them as text would corrupt them visibly.
var UnknownKeys = map[string][]byte{
	"orphan:ledger:v1:" + SourceID:      []byte("\x01ledger-v1\x00\xff\xfe" + destinationID),
	"connector:orphan:v1:" + SourceID:   []byte("\x01orphan\x00\xff\xfe" + SourceID),
	"future:subsystem:something-else:k": []byte("\x00\xff\xfeopaque"),
}

// SeedUnknownKeys writes UnknownKeys into db, simulating a database an
// unknown newer build left behind.
func SeedUnknownKeys(ctx context.Context, db database.DB) error {
	for k, v := range UnknownKeys {
		if err := db.Set(ctx, k, v); err != nil {
			return cerrors.Errorf("seed unknown key %q: %w", k, err)
		}
	}
	return nil
}

// AssertUnknownKeysIntact checks that every seeded unknown key is still in
// db with exactly the bytes it was written with, and that none of them is
// visible through a store's own key prefix. Surviving untouched is the
// point: the build that wrote them has to find them again, so ignoring
// them means leaving them alone, not deleting or rewriting them.
func AssertUnknownKeysIntact(t *testing.T, ctx context.Context, db database.DB) {
	t.Helper()

	for k, want := range UnknownKeys {
		got, err := db.Get(ctx, k)
		if err != nil {
			t.Fatalf("unknown key %q gone after a build that does not know it opened the database: %v", k, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("unknown key %q was rewritten: got %q, want %q", k, got, want)
		}
	}

	for _, prefix := range knownStorePrefixes {
		keys, err := db.GetKeys(ctx, prefix)
		if err != nil {
			t.Fatalf("list keys under %q: %v", prefix, err)
		}
		for k := range UnknownKeys {
			if slices.Contains(keys, k) {
				t.Fatalf("unknown key %q is listed under store prefix %q, so a store would try to decode it", k, prefix)
			}
		}
	}
}
