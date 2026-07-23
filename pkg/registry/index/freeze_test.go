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

package index_test

import (
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestCheckRollback_and_CheckStaleness_AreIndependentlyTriggerable proves
// the two freeze checks are separate, never-conflated fixtures (plan-v2
// §15.1): a rollback-only fixture must not report staleness, and vice
// versa.
func TestCheckRollback_and_CheckStaleness_AreIndependentlyTriggerable(t *testing.T) {
	is := is.New(t)

	t.Run("rollback_only", func(t *testing.T) {
		is := is.New(t)
		err := index.CheckRollback(5, 10)
		is.True(err != nil)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, index.CodeIndexRollback)
	})

	t.Run("staleness_only", func(t *testing.T) {
		is := is.New(t)
		old := time.Now().Add(-8 * 24 * time.Hour)
		err := index.CheckStaleness(old, time.Now(), index.DefaultMaxStaleness)
		is.True(err != nil)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, index.CodeIndexStale)
	})

	t.Run("neither_fires_when_fresh_and_monotonic", func(t *testing.T) {
		is := is.New(t)
		is.NoErr(index.CheckRollback(11, 10))
		is.NoErr(index.CheckStaleness(time.Now().Add(-time.Hour), time.Now(), index.DefaultMaxStaleness))
	})

	t.Run("equal_version_is_not_a_rollback", func(t *testing.T) {
		is := is.New(t)
		is.NoErr(index.CheckRollback(10, 10))
	})

	t.Run("exactly_at_staleness_boundary_is_not_stale", func(t *testing.T) {
		is := is.New(t)
		now := time.Now()
		is.NoErr(index.CheckStaleness(now.Add(-index.DefaultMaxStaleness), now, index.DefaultMaxStaleness))
	})
}
