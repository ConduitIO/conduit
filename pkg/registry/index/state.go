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
	"os"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/atomicfile"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// State is the locally persisted rollback high-water mark (R-1 §b item 1):
// the highest payload.index.version this client has successfully verified.
// It is intentionally the only thing this package persists — freshness
// (index.timestamp) is checked against wall-clock time on every fetch and
// carries no local state of its own.
type State struct {
	Version int64 `json:"version"`
}

// LoadState reads the persisted high-water mark from path. A missing file
// is not an error: it returns the zero State (Version: 0), matching "no
// index has ever been verified yet" — R-1 §b's documented gap that a
// client with no prior state has no rollback protection on its very first
// fetch (it falls back to the staleness check alone).
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, cerrors.Errorf("could not read index state %q: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, cerrors.Errorf("could not parse index state %q: %w", path, err)
	}
	return s, nil
}

// SaveState persists s to path atomically (temp file + rename in the same
// directory), so a crash mid-write can never leave a torn state file
// (Invariant 5) and can never corrupt the high-water mark into a value an
// attacker could exploit to widen the rollback window. Callers must only
// call SaveState after a fetch has passed every verification/freshness/
// rollback check (see CheckRollback's doc comment) — this function itself
// performs no such check; it is a pure persistence primitive.
func SaveState(path string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return cerrors.Errorf("could not marshal index state: %w", err)
	}
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return cerrors.Errorf("could not write index state %q: %w", path, err)
	}
	return nil
}
