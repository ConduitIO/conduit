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

package starthook

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// resolverFor treats every plugin in standalone as standalone and every
// other plugin as built-in.
func resolverFor(standalone ...string) Resolver {
	set := map[string]bool{}
	for _, s := range standalone {
		set[s] = true
	}
	f := func(p string) bool { return set[p] }
	return Resolver{Processor: f, Connector: f}
}

// panicResolver fails the test if anything is resolved.
func panicResolver(t *testing.T) Resolver {
	f := func(p string) bool {
		t.Fatalf("resolved plugin %q for a pipeline without %s", p, DebeziumEncodePlugin)
		return false
	}
	return Resolver{Processor: f, Connector: f}
}

const (
	pl      = "pl"
	wasm    = "standalone:my-wasm"
	builtin = "builtin:field.set"
	encode  = "builtin:debezium.encode@v0.1.0"
	pgPlug  = "builtin:postgres"
	kafka   = "builtin:kafka"
)

func proc(id, plugin string) Processor { return Processor{ID: id, Plugin: plugin} }

func src(id string, procs ...Processor) Connector {
	return Connector{ID: pl + ":" + id, Plugin: pgPlug, Index: 0, Processors: procs}
}

func dst(id string, procs ...Processor) Connector {
	return Connector{ID: pl + ":" + id, Plugin: kafka, Index: 1, Processors: procs}
}

func requireRefusal(t *testing.T, err error) *conduiterr.ConduitError {
	t.Helper()
	is := is.New(t)
	is.True(err != nil) // start must be refused
	ce, ok := conduiterr.Get(err)
	is.True(ok) // refusal must be coded
	is.Equal(ce.Code, CodeDebeziumStandaloneBeforeEncode)
	is.Equal(ce.Code.Reason(), "pipeline.debezium_standalone_before_encode")
	is.True(strings.Contains(ce.Suggestion, "built-in"))
	return ce
}

func TestCheckTopology_RefusesStandaloneBeforeEncode(t *testing.T) {
	testCases := []struct {
		name     string
		topo     Topology
		resolver Resolver
		wantPath string
		wantIn   []string
	}{{
		name: "pipeline processor before pipeline encoder",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Processors:   []Processor{proc(pl+":clean", wasm), proc(pl+":enc", encode)},
			Destinations: []Connector{dst("kafka")},
		},
		resolver: resolverFor(wasm),
		wantPath: "/processors/0",
		wantIn:   []string{`pipeline processor "clean"`, wasm, `"enc"`},
	}, {
		name: "source processor before destination encoder",
		topo: Topology{
			Sources:      []Connector{src("pg", proc(pl+":pg:clean", wasm))},
			Destinations: []Connector{dst("kafka", proc(pl+":kafka:enc", encode))},
		},
		resolver: resolverFor(wasm),
		wantPath: "/connectors/0/processors/0",
		wantIn:   []string{`processor "clean" on source connector "pg"`},
	}, {
		name: "destination processor before encoder on the same destination",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Destinations: []Connector{dst("kafka", proc(pl+":kafka:clean", builtin), proc(pl+":kafka:w", wasm), proc(pl+":kafka:enc", encode))},
		},
		resolver: resolverFor(wasm),
		wantPath: "/connectors/1/processors/1",
		wantIn:   []string{`processor "w" on destination connector "kafka"`},
	}, {
		name: "pipeline processor before destination encoder",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Processors:   []Processor{proc(pl+":w", wasm)},
			Destinations: []Connector{dst("kafka", proc(pl+":kafka:enc", encode))},
		},
		resolver: resolverFor(wasm),
		wantPath: "/processors/0",
	}, {
		name: "processor before source-level encoder",
		topo: Topology{
			Sources:      []Connector{src("pg", proc(pl+":pg:w", wasm), proc(pl+":pg:enc", encode))},
			Destinations: []Connector{dst("kafka")},
		},
		resolver: resolverFor(wasm),
		wantPath: "/connectors/0/processors/0",
	}, {
		name: "standalone source connector",
		topo: Topology{
			Sources:      []Connector{{ID: pl + ":pg", Plugin: "standalone:postgres", Processors: nil}},
			Destinations: []Connector{dst("kafka", proc(pl+":kafka:enc", encode))},
		},
		resolver: resolverFor("standalone:postgres"),
		wantPath: "/connectors/0",
		wantIn:   []string{`source connector "pg" uses standalone plugin "standalone:postgres"`},
	}, {
		name: "the encoder itself resolves to a standalone plugin",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Processors:   []Processor{proc(pl+":enc", "debezium.encode")},
			Destinations: []Connector{dst("kafka")},
		},
		resolver: resolverFor("debezium.encode"),
		wantPath: "/processors/0",
		wantIn:   []string{`pipeline processor "enc" uses standalone plugin "debezium.encode"`},
	}, {
		name: "processor with no prefix that a standalone plugin shadows",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Processors:   []Processor{proc(pl+":f", "field.set"), proc(pl+":enc", encode)},
			Destinations: []Connector{dst("kafka")},
		},
		resolver: resolverFor("field.set"),
		wantPath: "/processors/0",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			tc.topo.PipelineID = pl
			tc.topo.FromConfig = true
			ce := requireRefusal(t, CheckTopology(tc.topo, tc.resolver))
			is.Equal(ce.ConfigPath, tc.wantPath)
			is.True(strings.Contains(ce.Message, "pipeline "+pl))
			for _, w := range tc.wantIn {
				is.True(strings.Contains(ce.Message, w)) // message names the offender
			}
		})
	}
}

func TestCheckTopology_AllowsBuiltinPathsAndUnrelatedStandalone(t *testing.T) {
	testCases := []struct {
		name string
		topo Topology
	}{{
		name: "all built-in",
		topo: Topology{
			Sources:      []Connector{src("pg", proc(pl+":pg:a", builtin))},
			Processors:   []Processor{proc(pl+":b", builtin), proc(pl+":enc", encode)},
			Destinations: []Connector{dst("kafka")},
		},
	}, {
		name: "standalone processor after the encoder",
		topo: Topology{
			Sources:      []Connector{src("pg")},
			Processors:   []Processor{proc(pl+":enc", encode), proc(pl+":after", wasm)},
			Destinations: []Connector{dst("kafka", proc(pl+":kafka:after", wasm))},
		},
	}, {
		name: "standalone processor on a destination that does not encode",
		topo: Topology{
			Sources: []Connector{src("pg")},
			Destinations: []Connector{
				dst("kafka", proc(pl+":kafka:enc", encode)),
				{ID: pl + ":log", Plugin: "builtin:log", Index: 2, Processors: []Processor{proc(pl+":log:w", wasm)}},
			},
		},
	}, {
		name: "standalone processor on a source whose records never reach an encoder",
		topo: Topology{
			Sources: []Connector{
				src("pg", proc(pl+":pg:enc", encode)),
				{ID: pl + ":other", Plugin: pgPlug, Index: 1, Processors: []Processor{proc(pl+":other:w", wasm)}},
			},
			Destinations: []Connector{dst("kafka")},
		},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			tc.topo.PipelineID = pl
			is.NoErr(CheckTopology(tc.topo, resolverFor(wasm)))
		})
	}
}

// TestCheckTopology_NoEncoderResolvesNothing: the check adds no behavior to a
// pipeline without debezium.encode, standalone plugins or not.
func TestCheckTopology_NoEncoderResolvesNothing(t *testing.T) {
	is := is.New(t)
	topo := Topology{
		PipelineID:   pl,
		Sources:      []Connector{{ID: "s", Plugin: "standalone:x", Processors: []Processor{proc("a", wasm)}}},
		Processors:   []Processor{proc("b", wasm)},
		Destinations: []Connector{{ID: "d", Plugin: "standalone:y", Processors: []Processor{proc("c", wasm)}}},
	}
	is.NoErr(CheckTopology(topo, panicResolver(t)))
}

// TestCheckTopology_NamesEveryOffenderOnce: with several offenders and several
// encoders the message lists each offender once, in path order, and the
// config path is the first.
func TestCheckTopology_NamesEveryOffenderOnce(t *testing.T) {
	is := is.New(t)
	topo := Topology{
		PipelineID: pl,
		FromConfig: true,
		Sources:    []Connector{src("pg", proc(pl+":pg:a", wasm))},
		Processors: []Processor{proc(pl+":b", wasm), proc(pl+":enc1", encode)},
		Destinations: []Connector{
			dst("kafka", proc(pl+":kafka:enc2", encode)),
		},
	}
	ce := requireRefusal(t, CheckTopology(topo, resolverFor(wasm)))
	is.Equal(ce.ConfigPath, "/connectors/0/processors/0")
	is.Equal(strings.Count(ce.Message, `processor "a"`), 1)
	is.Equal(strings.Count(ce.Message, `processor "b"`), 1)
	is.True(strings.Index(ce.Message, `"a"`) < strings.Index(ce.Message, `"b"`))
	is.True(strings.Contains(ce.Message, `"enc1", "enc2"`))
}

// TestCheckTopology_APIPipelineHasNoConfigPath: an API-created pipeline has no
// config document, so the error carries no pointer into one.
func TestCheckTopology_APIPipelineHasNoConfigPath(t *testing.T) {
	is := is.New(t)
	topo := Topology{
		PipelineID:   "3f2c",
		Sources:      []Connector{{ID: "src-uuid", Plugin: pgPlug}},
		Processors:   []Processor{proc("proc-uuid", wasm), proc("enc-uuid", encode)},
		Destinations: []Connector{{ID: "dst-uuid", Plugin: kafka, Index: 1}},
	}
	ce := requireRefusal(t, CheckTopology(topo, resolverFor(wasm)))
	is.Equal(ce.ConfigPath, "")
	is.True(strings.Contains(ce.Message, `"proc-uuid"`))
	is.True(!strings.Contains(ce.Message, "/processors/"))
}

// TestCheckTopology_NamesOnlyAffectedEncoders: an encoder whose path is
// built-in is not named, even when another encoder's path is not.
func TestCheckTopology_NamesOnlyAffectedEncoders(t *testing.T) {
	is := is.New(t)
	topo := Topology{
		PipelineID: pl,
		Sources: []Connector{
			src("pg", proc(pl+":pg:clean", builtin), proc(pl+":pg:ok-enc", encode)),
			{ID: pl + ":pg2", Plugin: pgPlug, Index: 1, Processors: []Processor{proc(pl+":pg2:w", wasm), proc(pl+":pg2:bad-enc", encode)}},
		},
		Destinations: []Connector{{ID: pl + ":kafka", Plugin: kafka, Index: 2}},
	}
	ce := requireRefusal(t, CheckTopology(topo, resolverFor(wasm)))
	is.True(strings.Contains(ce.Message, `"bad-enc"`))
	is.True(!strings.Contains(ce.Message, `"ok-enc"`))
	is.True(strings.Contains(ce.Message, `processor "w" on source connector "pg2"`))
}
