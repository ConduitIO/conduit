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
	"fmt"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// DefaultMaxStaleness is the default freshness window (R-1 §b, OQ3
// resolution): a client refuses an index whose payload.index.timestamp is
// older than now-minus-this. Operator-overridable, similar in spirit to
// install.allowUnsigned. The nightly freshness re-sign (well inside this
// window) keeps a quiet-period index fresh without false refusals.
const DefaultMaxStaleness = 7 * 24 * time.Hour

// CheckRollback refuses (CodeIndexRollback) a fetched index whose version
// is lower than the highest this client has previously observed and
// verified. This is deliberately a separate, independently-triggerable
// check from CheckStaleness: rollback protection alone doesn't stop a
// frozen-but-never-rolled-back index (the highest version an attacker has
// ever served, just old); staleness alone doesn't stop a rollback to a
// recent-enough-to-pass-freshness older version. Both are required (R-1
// §b item 3).
//
// highWaterMark is the caller's persisted value (see State/LoadState); it
// must be updated ONLY after a fetch passes every check (signature, schema,
// rollback, staleness) — never on a rejected fetch, so an attacker can't
// ratchet the client's trusted floor forward with garbage. That update
// discipline is the caller's responsibility (this function only compares).
func CheckRollback(fetchedVersion, highWaterMark int64) error {
	if fetchedVersion < highWaterMark {
		return conduiterr.New(CodeIndexRollback, fmt.Sprintf(
			"index version %d is older than the last verified version %d — refusing a possible rollback",
			fetchedVersion, highWaterMark,
		))
	}
	return nil
}

// CheckStaleness refuses (CodeIndexStale) an index whose timestamp is
// older than now-maxStaleness. Distinct from CodeIndexUnreachable
// (fetch-layer failure) and CodeIndexRollback (monotonic-counter check) —
// see CheckRollback's doc comment for why both checks are required
// together.
func CheckStaleness(timestamp, now time.Time, maxStaleness time.Duration) error {
	if now.Sub(timestamp) > maxStaleness {
		return conduiterr.New(CodeIndexStale, fmt.Sprintf(
			"index timestamp %s is older than the max staleness window of %s",
			timestamp.Format(time.RFC3339), maxStaleness,
		))
	}
	return nil
}
