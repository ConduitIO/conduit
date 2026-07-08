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
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
)

// TestParseWithWarnings_MatchesParse is the AC-4 guard for `pipelines lint`:
// exposing the parser's warnings must NOT change the run/provisioning parse
// path. ParseWithWarnings must return the same pipelines and the same error as
// Parse for the same input — it only additionally returns the warnings that
// Parse's callers (via ParseConfigurations) merely log. TestParser_V1_Warnings
// separately locks that ParseConfigurations still logs those warnings unchanged.
//
// Pipelines are compared order-independently (normalizePipelines): the v1 config
// model stores pipelines/connectors/processors in Go maps (v1/model.go), and
// ToConfig iterates them to build slices, so two independent parses of the same
// file yield the same pipelines in a non-deterministic slice order. AC-4 is
// about "the same pipelines", not their incidental ordering — a raw
// element-wise DeepEqual across two separate parses flakes on that ordering.
func TestParseWithWarnings_MatchesParse(t *testing.T) {
	is := is.New(t)
	// pipelines1-success.yml is the fixture TestParser_V1_Warnings uses; it
	// parses successfully AND triggers several advisory warnings.
	const fixture = "./v1/testdata/pipelines1-success.yml"

	parseFile := func() ([]config.Pipeline, error) {
		f, err := os.Open(fixture)
		is.NoErr(err)
		defer f.Close()
		return NewParser(log.Nop()).Parse(context.Background(), f)
	}

	parsePipelines, parseErr := parseFile()

	f, err := os.Open(fixture)
	is.NoErr(err)
	defer f.Close()
	withWarnPipelines, warns, withWarnErr := NewParser(log.Nop()).ParseWithWarnings(context.Background(), f)

	// Same error outcome.
	is.Equal(parseErr == nil, withWarnErr == nil)

	// Same pipelines (identical parse result — run path unchanged), compared
	// order-independently since the v1 map-backed model yields a
	// non-deterministic slice order per parse.
	normalizePipelines(parsePipelines)
	normalizePipelines(withWarnPipelines)
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

// normalizePipelines sorts a parsed pipeline slice — and every connector,
// processor, and connector-nested processor within it — by ID, so two parses
// of the same config (which the v1 map-backed model orders non-deterministically)
// compare equal under reflect.DeepEqual. IDs are unique within their scope, so
// the sort is total.
func normalizePipelines(ps []config.Pipeline) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	for i := range ps {
		sort.Slice(ps[i].Processors, func(a, b int) bool { return ps[i].Processors[a].ID < ps[i].Processors[b].ID })
		sort.Slice(ps[i].Connectors, func(a, b int) bool { return ps[i].Connectors[a].ID < ps[i].Connectors[b].ID })
		for ci := range ps[i].Connectors {
			conn := ps[i].Connectors[ci]
			sort.Slice(conn.Processors, func(a, b int) bool { return conn.Processors[a].ID < conn.Processors[b].ID })
		}
	}
}
