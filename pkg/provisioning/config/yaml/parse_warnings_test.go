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

package yaml

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// TestParseWithWarnings_MatchesParse is the AC-4 guard for `pipelines lint`:
// exposing the parser's warnings must NOT change the run/provisioning parse
// path. ParseWithWarnings must return exactly the same pipelines and the same
// error as Parse for the same input — it only additionally returns the warnings
// that Parse's callers (via ParseConfigurations) merely log. TestParser_V1_Warnings
// separately locks that ParseConfigurations still logs those warnings unchanged.
func TestParseWithWarnings_MatchesParse(t *testing.T) {
	is := is.New(t)
	// pipelines1-success.yml is the fixture TestParser_V1_Warnings uses; it
	// parses successfully AND triggers several advisory warnings.
	const fixture = "./v1/testdata/pipelines1-success.yml"

	parseFile := func() ([]any, error) {
		f, err := os.Open(fixture)
		is.NoErr(err)
		defer f.Close()
		pipelines, err := NewParser(log.Nop()).Parse(context.Background(), f)
		anys := make([]any, len(pipelines))
		for i, p := range pipelines {
			anys[i] = p
		}
		return anys, err
	}

	parsePipelines, parseErr := parseFile()

	f, err := os.Open(fixture)
	is.NoErr(err)
	defer f.Close()
	withWarnPipelines, warns, withWarnErr := NewParser(log.Nop()).ParseWithWarnings(context.Background(), f)

	// Same error outcome.
	is.Equal(parseErr == nil, withWarnErr == nil)

	// Same pipelines (identical parse result — run path unchanged).
	is.Equal(len(parsePipelines), len(withWarnPipelines))
	for i := range parsePipelines {
		is.True(reflect.DeepEqual(parsePipelines[i], withWarnPipelines[i]))
	}

	// And ParseWithWarnings additionally surfaces the advisory warnings.
	is.True(len(warns) > 0)
	for _, w := range warns {
		is.True(w.Message != "") // every warning carries a message
	}
}
