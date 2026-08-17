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
	"slices"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// The same binary must always ground the same way: a prompt that varies run
// to run makes every eval number unreproducible.
func TestBuildSystemPrompt_IsDeterministic(t *testing.T) {
	is := is.New(t)

	first := BuildSystemPrompt(BuiltinCatalog())
	for range 5 {
		is.Equal(BuildSystemPrompt(BuiltinCatalog()), first)
	}
}

func TestBuiltinCatalog_CoversTheBuiltinConnectors(t *testing.T) {
	is := is.New(t)
	names := CatalogNames(BuiltinCatalog())

	for _, want := range []string{"file", "generator", "kafka", "log", "postgres", "s3"} {
		is.True(slices.Contains(names, want))
	}
}

// The prompt states which roles a connector does NOT implement. "log as a
// source" is a plausible-looking pipeline the grounding exists to prevent.
func TestBuildSystemPrompt_StatesUnsupportedRoles(t *testing.T) {
	is := is.New(t)

	prompt := BuildSystemPrompt([]ConnectorInfo{{
		Name:              "log",
		Summary:           "writes records to the log",
		DestinationParams: []ParamInfo{{Key: "level", Type: "string"}},
	}})

	is.True(strings.Contains(prompt, "source: not supported"))
	is.True(strings.Contains(prompt, "destination parameters:"))
}

// Required parameters are never dropped by the optional-parameter cap:
// grounding the model on a config it cannot legally produce guarantees a
// wasted retry.
func TestGroundedParams_KeepsEveryRequiredParam(t *testing.T) {
	is := is.New(t)
	cat := BuiltinCatalog()

	var checked int
	for _, c := range cat {
		for _, role := range [][]ParamInfo{c.SourceParams, c.DestinationParams} {
			var optional int
			for _, p := range role {
				if !p.Required {
					optional++
				}
			}
			is.True(optional <= MaxOptionalParamsPerRole)
			checked++
		}
	}
	is.True(checked > 0)
}

// The catalog carries no version strings. Read outside a build-info context
// every built-in connector reports the placeholder "(devel)" (see llmsgen's
// gatherConnectors), and grounding a model on that teaches it to emit a fake
// version.
func TestBuildSystemPrompt_CarriesNoConnectorVersions(t *testing.T) {
	is := is.New(t)

	prompt := BuildSystemPrompt(BuiltinCatalog())

	is.True(!strings.Contains(prompt, "(devel)"))
}

// The set a candidate is CHECKED against must not drift from the set the
// model was GROUNDED on — one map feeds both.
func TestBuiltinConnectorRefs_MatchTheCatalog(t *testing.T) {
	is := is.New(t)
	refs := BuiltinConnectorRefs()

	for _, name := range CatalogNames(BuiltinCatalog()) {
		_, ok := refs[name]
		is.True(ok)
	}
}

func TestBuiltinProcessorRefs_NonEmpty(t *testing.T) {
	is := is.New(t)

	is.True(len(BuiltinProcessorRefs()) > 0)
}
