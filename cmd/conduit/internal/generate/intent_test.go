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

package generate

import (
	"context"
	"slices"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func TestExtractIntent_DirectionAndCapabilities(t *testing.T) {
	is := is.New(t)
	names := CatalogNames(BuiltinCatalog())

	for _, tc := range []struct {
		name         string
		prompt       string
		source       string
		destination  string
		capabilities []string
	}{{
		name:         "the canonical case from the design doc",
		prompt:       "stream new orders from postgres into a kafka topic, only orders over $100",
		source:       "postgres",
		destination:  "kafka",
		capabilities: []string{"filter"},
	}, {
		name:         "export phrasing with an encoding",
		prompt:       "export all customer records from postgres to an s3 bucket as json",
		source:       "postgres",
		destination:  "s3",
		capabilities: []string{"json-encode"},
	}, {
		name:        "aliases resolve to catalog names",
		prompt:      "read from pg and write to stdout",
		source:      "postgres",
		destination: "log",
	}, {
		name:        "reversed direction is read as reversed",
		prompt:      "move data from kafka into postgres",
		source:      "kafka",
		destination: "postgres",
	}, {
		name:        "only one side named leaves the other empty",
		prompt:      "get everything out of postgres somewhere useful",
		source:      "postgres",
		destination: "",
	}, {
		name:   "no connector named extracts nothing",
		prompt: "keep my search index fresh",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIntent(tc.prompt, names)
			is.Equal(got.Source, tc.source)
			is.Equal(got.Destination, tc.destination)
			for _, want := range tc.capabilities {
				is.True(slices.Contains(got.Capabilities, want))
			}
			is.Equal(got.Contradiction, "")
		})
	}
}

// A mention with no directional cue nearby assigns no role. Guessing would
// turn "compare postgres and kafka throughput" into a pipeline request.
func TestExtractIntent_NoCueMeansNoRole(t *testing.T) {
	is := is.New(t)

	got := ExtractIntent("compare postgres and kafka", CatalogNames(BuiltinCatalog()))

	is.Equal(got.Source, "")
	is.Equal(got.Destination, "")
}

// One role claimed by two different connectors is unsatisfiable by any
// candidate, so it is the one extraction outcome that refuses up front.
func TestExtractIntent_ContradictionIsDetected(t *testing.T) {
	is := is.New(t)

	got := ExtractIntent("read from kafka, source is postgres, write to the log", CatalogNames(BuiltinCatalog()))

	is.True(got.Contradiction != "")
}

// Every alias must name a connector this binary actually has, or the checker
// would demand a connector the model cannot legally produce.
func TestConnectorAliases_ResolveToRealConnectors(t *testing.T) {
	is := is.New(t)
	names := CatalogNames(BuiltinCatalog())

	for alias, canonical := range connectorAliases {
		is.True(slices.Contains(names, canonical)) // alias -> real connector
		is.True(alias != "")
	}
}

// Every capability phrase must map to a tag capability.go can satisfy: an
// unknown tag is permanently unsatisfiable, so a typo here would fail every
// candidate forever.
func TestCapabilityPhrases_ResolveToRealTags(t *testing.T) {
	is := is.New(t)

	for phrase, tag := range capabilityPhrases {
		_, ok := capabilityProcessors[tag]
		is.True(ok)
		is.True(phrase != "")
	}
}

// Pre-call refusal is narrow by design: an empty prompt and a contradictory
// one are refused before a provider call; a terse one is not.
func TestGenerate_PreCallRefusalIsNarrow(t *testing.T) {
	is := is.New(t)

	t.Run("empty prompt never calls the provider", func(t *testing.T) {
		p := &fakeProvider{replies: []string{validCandidate}}
		_, err := Generate(context.Background(), Input{Prompt: "   ", Provider: p})
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeAmbiguousPrompt)
		is.Equal(len(p.requests), 0)
	})

	t.Run("contradictory prompt never calls the provider", func(t *testing.T) {
		p := &fakeProvider{replies: []string{validCandidate}}
		_, err := Generate(context.Background(), Input{
			Prompt:   "read from kafka, source is postgres, write to the log",
			Provider: p,
		})
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeAmbiguousPrompt)
		is.Equal(len(p.requests), 0)
	})

	t.Run("terse prompt is sent to the provider, not refused", func(t *testing.T) {
		p := &fakeProvider{replies: []string{validCandidate}}
		_, err := Generate(context.Background(), Input{Prompt: "generator to log", Provider: p})
		is.NoErr(err)
		is.Equal(len(p.requests), 1)
	})
}

// A schema-valid pipeline that does the wrong thing is corrected inside the
// budget, then succeeds — the failure mode the whole checker exists for.
func TestGenerate_SemanticMismatchIsCorrectedInsideTheBudget(t *testing.T) {
	is := is.New(t)
	// Asked for postgres -> log; the first candidate reads from the generator.
	p := &fakeProvider{replies: []string{validCandidate, postgresToLogCandidate}}

	res, err := Generate(context.Background(), Input{
		Prompt:   "read from postgres and write to the log",
		Provider: p,
	})

	is.NoErr(err)
	is.Equal(len(p.requests), 2)
	is.True(!res.Attempts[0].Semantic.Match)
	is.True(res.Attempts[0].Report.OK()) // it validated — that is the point
	retry := p.requests[1].Prompt
	is.True(len(res.Attempts[0].Feedback.Items) > 0)
	is.True(retry != p.requests[0].Prompt) // the correction was attached
}

// Exhausting the budget on intent mismatches is its own code: the config is
// usable, it just isn't what was asked for, so the fix is different.
func TestGenerate_ExhaustedSemantic_IsSemanticMismatch(t *testing.T) {
	is := is.New(t)
	p := &fakeProvider{replies: []string{validCandidate}}

	res, err := Generate(context.Background(), Input{
		Prompt:      "read from postgres and write to kafka",
		Provider:    p,
		MaxAttempts: 2,
	})

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeSemanticMismatch)
	is.True(res.Report.OK())     // the artifact is valid
	is.True(res.Candidate != "") // and is not discarded
}

// When the prompt names no connector, a candidate is accepted if its
// connectors are traceable to the user's words, and rejected when they are
// not — the post-hoc half of ambiguity (design §9).
func TestGenerate_PostHocAmbiguity(t *testing.T) {
	is := is.New(t)

	t.Run("untraceable connectors are a mismatch", func(t *testing.T) {
		p := &fakeProvider{replies: []string{validCandidate}}
		_, err := Generate(context.Background(), Input{
			Prompt:      "keep my search index fresh",
			Provider:    p,
			MaxAttempts: 1,
		})
		is.True(err != nil)
	})

	t.Run("a connector named without a direction still traces", func(t *testing.T) {
		p := &fakeProvider{replies: []string{validCandidate}}
		_, err := Generate(context.Background(), Input{
			Prompt:   "something involving the generator and the log",
			Provider: p,
		})
		is.NoErr(err)
	})
}

// postgresToLogCandidate reads from postgres, so it matches a "from postgres
// to the log" request where validCandidate does not.
const postgresToLogCandidate = `version: "2.2"
pipelines:
  - id: pg-to-log
    status: running
    connectors:
      - id: source
        type: source
        plugin: "builtin:postgres"
        settings:
          url: postgres://user:pass@localhost:5432/db?sslmode=disable
          tables: orders
      - id: destination
        type: destination
        plugin: "builtin:log"
        settings:
          level: info
`
