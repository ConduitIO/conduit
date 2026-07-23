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

package adversarial_test

import (
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/trust/adversarial"
)

// TestCorpus_EveryFixtureMatchesItsWantErr is the corpus's own self-test:
// every fixture Corpus() produces, run through adversarial.Run (the same
// dispatch index-CI's pinned-module import would use), must match its
// declared WantErr exactly. This is what makes "index-CI runs the identical
// corpus" a tested property rather than an assumption (plan-v2 §11, P1-4).
func TestCorpus_EveryFixtureMatchesItsWantErr(t *testing.T) {
	is := is.New(t)

	fixtures, err := adversarial.Corpus()
	is.NoErr(err)
	is.True(len(fixtures) >= 6) // happy-path signature + happy-path provenance + 4 adversarial cases

	seenNames := map[string]bool{}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			is := is.New(t)
			is.True(!seenNames[f.Name]) // fixture names must be unique
			seenNames[f.Name] = true

			err := adversarial.Run(f)
			is.NoErr(err) // Run itself reports a mismatch as an error
		})
	}
}
