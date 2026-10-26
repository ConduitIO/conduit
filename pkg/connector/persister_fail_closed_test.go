// Copyright © 2022 Meroxa, Inc.
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

package connector

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// TestFailMissingResults_MissingOutcomeIsAnError pins invariant 1 at the
// guard in failMissingResults: a connector whose write outcome was not
// recorded must reach its callback as an error, never as nil (a nil means
// "state committed"). Found by guardmut: deleting the guard left every test
// green, because writeBatch always records an outcome.
func TestFailMissingResults_MissingOutcomeIsAnError(t *testing.T) {
	is := is.New(t)

	batch := map[string]persistData{"committed": {}, "missing": {}, "failed": {}}
	boom := cerrors.New("store write failed")
	results := map[string]error{"committed": nil, "failed": boom}

	failed := failMissingResults(batch, results)

	is.Equal(failed, 2)                // "failed" and "missing"
	is.NoErr(results["committed"])     // a recorded commit stays nil
	is.Equal(results["failed"], boom)  // a recorded failure is not rewritten
	is.True(results["missing"] != nil) // no outcome must not read as success
}
