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
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/boundedfetch"
)

// MaxIndexBytes caps a fetched index document's size (P0-2, plan-v2 §2.4
// item 1) — generous for a catalog of dozens-to-low-hundreds of connectors;
// tune before ship.
const MaxIndexBytes int64 = 8 * 1024 * 1024 // 8 MiB

// Fetch retrieves the raw index bytes from url over HTTP(S), enforcing
// MaxIndexBytes. It performs no parsing and no trust decision — see
// ParseUnverified for the next step. A response exceeding the cap fails
// with CodeIndexTooLarge, distinct from any other fetch failure
// (CodeIndexUnreachable), so a caller can tell "the origin sent too much"
// from "the origin was unreachable".
func Fetch(ctx context.Context, url string) ([]byte, error) {
	data, err := boundedfetch.Fetch(ctx, url, MaxIndexBytes)
	if err != nil {
		if cerrors.Is(err, boundedfetch.ErrTooLarge) {
			return nil, conduiterr.Wrap(CodeIndexTooLarge, "index exceeds the maximum allowed size", err)
		}
		return nil, conduiterr.Wrap(CodeIndexUnreachable, "could not fetch the connector registry index", err)
	}
	return data, nil
}

// FetchFile reads the raw index bytes from a local path (offline/bundle
// mode), enforcing the same MaxIndexBytes cap as Fetch.
func FetchFile(path string) ([]byte, error) {
	data, err := boundedfetch.ReadFile(path, MaxIndexBytes)
	if err != nil {
		if cerrors.Is(err, boundedfetch.ErrTooLarge) {
			return nil, conduiterr.Wrap(CodeIndexTooLarge, "index file exceeds the maximum allowed size", err)
		}
		return nil, conduiterr.Wrap(CodeIndexUnreachable, "could not read the connector registry index file", err)
	}
	return data, nil
}
