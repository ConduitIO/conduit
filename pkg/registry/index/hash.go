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
	"crypto/sha256"
	"encoding/hex"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// HashConnectors returns "sha256:<hex>" over the JCS-canonicalized
// connectors[] array — the value persisted in State.LastVerifiedConnectorsHash
// and compared by Verify's freshness-only acceptance path (R-1 §a.2.c): a
// freshness signature may only extend index.timestamp/index.version over
// BYTE-IDENTICAL connectors[] content, never authorize different content on
// its own. Hashing (rather than comparing raw canonical bytes directly) keeps
// State small and gives a stable, loggable value for diagnostics.
//
// The hash is computed over the canonicalized connectors array alone (not the
// full payload, which also carries index.version/timestamp that legitimately
// change on every heartbeat re-sign) — hashing the whole payload would make
// every nightly freshness re-sign look like new content and defeat the
// byte-identical check this function exists to support.
func HashConnectors(connectors []Connector) (string, error) {
	raw, err := json.Marshal(connectors)
	if err != nil {
		return "", cerrors.Errorf("could not marshal connectors for hashing: %w", err)
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return "", cerrors.Errorf("could not canonicalize connectors for hashing: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
