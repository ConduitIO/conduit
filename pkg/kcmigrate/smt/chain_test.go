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

package smt_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt/smttest"
	"github.com/matryer/is"
)

const pgClass = "io.debezium.connector.postgresql.PostgresConnector"

func data(t *testing.T) *smt.Data {
	t.Helper()
	d, err := smt.BuiltinData()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// decisions classifies kvs (key, value pairs) as a Debezium Postgres
// connector with the production rules, so the SMT keys carry the shared
// file's smt claim.
func decisions(t *testing.T, kvs ...string) []rules.Decision {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	c := kcmigrate.Connector{Name: "c", Class: pgClass, File: "c.properties"}
	for i := 0; i < len(kvs); i += 2 {
		c.Config = append(c.Config, kcmigrate.KV{
			Key: kvs[i], Value: kcmigrate.NewValue(kvs[i+1]),
			Origin: kcmigrate.Origin{File: "c.properties", Line: i/2 + 1, Column: 1},
		})
	}
	return e.Classify(c).Decisions
}

var sourceEnv = smt.Env{
	Scope: rules.ScopeSource, Source: "builtin:postgres", Destination: "builtin:kafka",
	ProcessorPath: "/pipelines/0/connectors/0/processors",
}

func apply(t *testing.T, reg *smt.Registry, env smt.Env, kvs ...string) (smt.Outcome, []rules.Decision) {
	t.Helper()
	ds := decisions(t, kvs...)
	out, err := smt.Apply(data(t), reg, env, ds)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	checkContract(t, ds)
	return out, ds
}

// checkContract: every entry satisfies the report contract.
func checkContract(t *testing.T, ds []rules.Decision) {
	t.Helper()
	rep := kcmigrate.NewReport("test")
	rep.Connectors = append(rep.Connectors, kcmigrate.ReportConnector{Name: "c", Entries: rules.Entries(ds)})
	rep.Summarize()
	if err := rep.Validate(); err != nil {
		t.Fatal(err)
	}
}

func entry(ds []rules.Decision, key string) kcmigrate.Entry {
	var e kcmigrate.Entry
	for _, d := range ds {
		if d.KV.Key == key {
			e = d.Entry
		}
	}
	return e
}

type want struct {
	status   kcmigrate.Status
	code     string
	blocking bool
}

func got(e kcmigrate.Entry) want { return want{e.Status, e.Code, e.Blocking} }

var (
	ok      = want{kcmigrate.StatusTranslated, "", false}
	refused = want{kcmigrate.StatusUnsupported, "kc.smt_unsupported", true}
	unknown = want{kcmigrate.StatusUnsupported, "kc.key_unknown", true}
)

func TestBuiltinData(t *testing.T) {
	is := is.New(t)
	d := data(t)
	is.True(len(d.Capabilities()) > 0)
}

// TestBuiltin_ClaimsNoSMT: no SMT is translated before its translator ships
// with oracle goldens (design, "Tests"); B3 is the framework only.
func TestBuiltin_ClaimsNoSMT(t *testing.T) {
	is := is.New(t)
	reg := smt.Builtin()
	is.Equal(len(reg.Transforms), 0)
	is.Equal(len(reg.Predicates), 0)
}

func TestApply_EmptyLists(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smt.Builtin(), sourceEnv, "transforms", "", "predicates", " ")
	is.Equal(got(entry(ds, "transforms")), ok)
	is.Equal(got(entry(ds, "predicates")), ok)
	is.Equal(len(out.Processors), 0)
	is.Equal(len(out.Transforms), 0)
	is.True(!out.Consulted)
}

func TestApply_NoChainKeys(t *testing.T) {
	is := is.New(t)
	out, _ := apply(t, smt.Builtin(), sourceEnv, "tasks.max", "1")
	is.Equal(out, smt.Outcome{Transforms: []kcmigrate.Transform{}})
}

// TestApply_Grouping covers the cases the design decides before any
// translator runs ("Grouping").
func TestApply_Grouping(t *testing.T) {
	reg := smttest.Registry()
	tests := []struct {
		name string
		kvs  []string
		want map[string]want
	}{
		{
			"alias with no type blocks",
			[]string{"transforms", "a", "transforms.a.x", "1"},
			map[string]want{"transforms": refused, "transforms.a.x": refused},
		},
		{
			"empty type blocks",
			[]string{"transforms", "a", "transforms.a.type", " "},
			map[string]want{"transforms": refused, "transforms.a.type": refused},
		},
		{
			"unlisted alias is a no-op",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.b.type", "com.example.Custom", "transforms.b.x", "1"},
			map[string]want{"transforms": ok, "transforms.a.type": ok, "transforms.b.type": ok, "transforms.b.x": ok},
		},
		{
			"no transforms key: every setting is a no-op",
			[]string{"transforms.a.type", "com.example.Custom"},
			map[string]want{"transforms.a.type": ok},
		},
		{
			"alias listed twice blocks the chain",
			[]string{"transforms", "a, a", "transforms.a.type", smttest.Drop},
			map[string]want{"transforms": refused, "transforms.a.type": refused},
		},
		{
			"undefined predicate blocks",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.predicate", "p"},
			map[string]want{"transforms": refused, "transforms.a.type": refused, "transforms.a.predicate": refused},
		},
		{
			"unused predicate is a no-op",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "predicates", "p", "predicates.p.type", "com.example.Custom", "predicates.p.x", "1"},
			map[string]want{"transforms": ok, "transforms.a.type": ok, "predicates": ok, "predicates.p.type": ok, "predicates.p.x": ok},
		},
		{
			"listed predicate without a type blocks",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "predicates", "p"},
			map[string]want{"transforms": refused, "transforms.a.type": refused, "predicates": refused},
		},
		{
			"unlisted predicate settings are a no-op",
			[]string{"predicates.q.type", "x"},
			map[string]want{"predicates.q.type": ok},
		},
		{
			"config-provider reference as an option",
			[]string{"transforms", "a", "transforms.a.type", smttest.Split, "transforms.a.a", "${file:/s.properties:f}"},
			map[string]want{"transforms": refused, "transforms.a.type": refused, "transforms.a.a": refused},
		},
		{
			"config-provider reference as the list",
			[]string{"transforms", "${env:T}", "transforms.a.type", smttest.Drop},
			map[string]want{"transforms": refused, "transforms.a.type": refused},
		},
		{
			"negate is a boolean",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.negate", "yes"},
			map[string]want{"transforms": refused, "transforms.a.negate": refused, "transforms.a.type": refused},
		},
		{
			"negate without a predicate is ignored",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.negate", "TRUE"},
			map[string]want{"transforms": ok, "transforms.a.negate": ok},
		},
		{
			"unclaimed option is unknown and refuses the chain",
			[]string{"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.newer.option", "1"},
			map[string]want{"transforms": refused, "transforms.a.type": refused, "transforms.a.newer.option": unknown},
		},
		{
			// Kafka Connect hands transforms.a.b.* to a (as options b.*) and
			// to a.b: both are refused.
			"an alias that is a dotted prefix of another refuses both",
			[]string{"transforms", "a,a.b", "transforms.a.type", smttest.Drop, "transforms.a.b.type", smttest.Drop},
			map[string]want{"transforms": refused, "transforms.a.type": refused, "transforms.a.b.type": refused},
		},
		{
			"aliases that only share a prefix are not nested",
			[]string{"transforms", "a,ab", "transforms.a.type", smttest.Drop, "transforms.ab.type", smttest.Drop},
			map[string]want{"transforms": ok, "transforms.a.type": ok, "transforms.ab.type": ok},
		},
		{
			"a nested predicate pair refuses both, used or not",
			[]string{
				"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.predicate", "p",
				"predicates", "p,p.k", "predicates.p.type", smttest.Always, "predicates.p.k.type", smttest.Always,
			},
			map[string]want{"transforms": refused, "transforms.a.predicate": refused, "predicates": refused, "predicates.p.type": refused, "predicates.p.k.type": refused},
		},
		{
			// Recorded before any refusal: a predicate whose only user is
			// refused early is not reported as unused.
			"predicate of an early-refused link is not unused",
			[]string{"transforms", "a", "transforms.a.predicate", "p", "predicates", "p", "predicates.p.type", smttest.Always},
			map[string]want{"transforms": refused, "transforms.a.predicate": refused, "predicates": refused, "predicates.p.type": refused},
		},
		{
			// Java's String.trim keeps U+00A0, so the alias is "a\u00a0" and
			// transforms.a.type belongs to an unlisted alias.
			"Java trim keeps a no-break space",
			[]string{"transforms", "a\u00a0", "transforms.a.type", smttest.Drop},
			map[string]want{"transforms": refused, "transforms.a.type": ok},
		},
		{
			"Java \\s includes the vertical tab",
			[]string{"transforms", "a\v,b", "transforms.a.type", smttest.Drop, "transforms.b.type", smttest.Drop},
			map[string]want{"transforms": ok, "transforms.a.type": ok, "transforms.b.type": ok},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			out, ds := apply(t, reg, sourceEnv, tt.kvs...)
			for k, w := range tt.want {
				is.Equal(got(entry(ds, k)), w) // key k
			}
			blocks := false
			for _, d := range ds {
				blocks = blocks || (d.Entry.Blocking && strings.HasPrefix(d.KV.Key, "transforms"))
			}
			if blocks {
				is.Equal(len(out.Processors), 0) // all or nothing
			}
		})
	}
}

func TestApply_RefusalsFromData(t *testing.T) {
	tests := []struct {
		class, code, mention string
	}{
		{"io.debezium.transforms.partitions.PartitionRouting", "kc.target_capability_missing", "partitions by key only"},
		{"org.apache.kafka.connect.transforms.Filter", "kc.smt_unsupported", "until a migrate release translates it"},
		{"org.apache.kafka.connect.transforms.ExtractField$Key", "kc.smt_unsupported", "until a migrate release translates it"},
		{"org.apache.kafka.connect.transforms.Cast$Value", "kc.smt_unsupported", "v0.23"},
		{"org.apache.kafka.connect.transforms.DropHeaders", "kc.smt_unsupported", "v0.23"},
		{"com.example.CustomTransform", "kc.smt_unsupported", "processor SDK"},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			is := is.New(t)
			out, ds := apply(t, smt.Builtin(), sourceEnv, "transforms", "t", "transforms.t.type", tt.class)
			e := entry(ds, "transforms.t.type")
			is.Equal(got(e), want{kcmigrate.StatusUnsupported, tt.code, true})
			is.True(strings.Contains(e.Reason+" "+e.Fix, tt.mention))
			is.Equal(out.Transforms, []kcmigrate.Transform{{Alias: "t", Class: tt.class, Status: kcmigrate.StatusUnsupported}})
			is.Equal(len(out.Processors), 0)
		})
	}
}

func TestApply_TranslatedChain(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smttest.Registry(), sourceEnv,
		"transforms", "drop, split",
		"transforms.drop.type", smttest.Drop,
		"transforms.drop.predicate", "isX",
		"transforms.drop.negate", "true",
		"transforms.split.type", smttest.Split+"$Value",
		"transforms.split.a", "x",
		"transforms.split.b", "y",
		"predicates", "isX",
		"predicates.isX.type", smttest.HasMeta,
		"predicates.isX.key", "x",
	)
	guard := `ne (printf "%T" .Payload.After) "<nil>"`
	is.Equal(out.Processors, []smt.Emitted{
		{ID: "smt-drop", Plugin: "filter", Condition: `{{ not (hasKey .Metadata "x") }}`, Path: "/pipelines/0/connectors/0/processors/0"},
		{
			ID: "smt-schema-value", Plugin: "field.exclude", Condition: "{{ " + guard + " }}",
			Settings: map[string]string{"fields": `.Metadata["opencdc.payload.schema.subject"],.Metadata["opencdc.payload.schema.version"]`},
			Path:     "/pipelines/0/connectors/0/processors/1",
		},
		{ID: "smt-split-1", Plugin: "field.exclude", Condition: "{{ " + guard + " }}", Settings: map[string]string{"fields": ".Payload.After.x"}, Path: "/pipelines/0/connectors/0/processors/2"},
		{ID: "smt-split-2", Plugin: "field.exclude", Condition: "{{ " + guard + " }}", Settings: map[string]string{"fields": ".Payload.After.y"}, Path: "/pipelines/0/connectors/0/processors/3"},
	})
	for _, d := range ds {
		is.Equal(got(d.Entry), ok) // every key translated
	}
	is.Equal(entry(ds, "transforms.split.b").Target.ConfigPath, "/pipelines/0/connectors/0/processors/3")
	is.Equal(entry(ds, "transforms.split.type").Target.ConfigPath, "/pipelines/0/connectors/0/processors/2")
	is.Equal(entry(ds, "predicates.isX.key").Target.ConfigPath, "/pipelines/0/connectors/0/processors/0")
	is.Equal(out.Transforms, []kcmigrate.Transform{
		{Alias: "drop", Class: smttest.Drop, Status: kcmigrate.StatusTranslated, Processor: "filter"},
		{Alias: "split", Class: smttest.Split + "$Value", Status: kcmigrate.StatusTranslated, Processor: "field.exclude"},
	})
	is.True(out.Consulted)
}

func TestApply_ProcessorOffsetAndIDs(t *testing.T) {
	is := is.New(t)
	env := sourceEnv
	env.ProcessorOffset = 2
	out, _ := apply(t, smttest.Registry(), env,
		"transforms", "a/b,a_b", "transforms.a/b.type", smttest.Drop, "transforms.a_b.type", smttest.Drop)
	is.Equal(len(out.Processors), 2)
	is.Equal(out.Processors[0].ID, "smt-a_b")
	is.Equal(out.Processors[1].ID, "smt-a_b-2") // a collision gets a suffix in alias order
	is.Equal(out.Processors[1].Path, "/pipelines/0/connectors/0/processors/3")
}

// TestApply_AllOrNothing: one refused link means no processor for the
// whole chain, and every other link says which link refused it.
func TestApply_AllOrNothing(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smttest.Registry(), sourceEnv,
		"transforms", "drop,r,split",
		"transforms.drop.type", smttest.Drop,
		"transforms.r.type", smttest.Refuse,
		"transforms.r.mode", "x",
		"transforms.split.type", smttest.Split,
		"transforms.split.a", "f",
	)
	is.Equal(len(out.Processors), 0)
	for _, k := range []string{"transforms", "transforms.drop.type", "transforms.r.type", "transforms.r.mode", "transforms.split.type", "transforms.split.a"} {
		is.Equal(got(entry(ds, k)), refused) // key k
	}
	is.True(strings.Contains(entry(ds, "transforms.drop.type").Reason, "the transform r in the same chain is not"))
	is.True(strings.Contains(entry(ds, "transforms.drop.type").Fix, "--accept-gaps"))
	is.Equal(entry(ds, "transforms.r.mode").Reason, "Refuse refuses every mode.")
	is.True(strings.Contains(entry(ds, "transforms.r.type").Reason, "Part of the transform r"))
	is.True(strings.Contains(entry(ds, "transforms").Reason, "transform r: Refuse refuses every mode"))
	for _, tr := range out.Transforms {
		is.Equal(tr.Status, kcmigrate.StatusUnsupported)
		is.Equal(tr.Processor, "")
	}
}

func TestApply_PredicateRefused(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smttest.Registry(), sourceEnv,
		"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.a.predicate", "p",
		"predicates", "p", "predicates.p.type", smttest.Never)
	is.Equal(len(out.Processors), 0)
	is.Equal(entry(ds, "predicates.p.type").Reason, "Never is never translated.")
	is.True(strings.Contains(entry(ds, "transforms.a.predicate").Reason, `predicate "p" is not translated`))
	is.Equal(got(entry(ds, "predicates")), refused)
}

// TestApply_PredicateOnNoProcessor: a predicate has nothing to become the
// condition of when the transform writes no processor.
func TestApply_PredicateOnNoProcessor(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smttest.Registry(), sourceEnv,
		"transforms", "r", "transforms.r.type", smttest.Refuse, "transforms.r.predicate", "p",
		"predicates", "p", "predicates.p.type", smttest.Always)
	is.Equal(len(out.Processors), 0)
	is.Equal(got(entry(ds, "transforms.r.predicate")), refused)
	is.True(strings.Contains(entry(ds, "transforms.r.predicate").Reason, "writes no processor"))
	is.True(strings.Contains(entry(ds, "predicates.p.type").Reason, "whose chain is not translated"))
}

func TestApply_NoProcessorTranslation(t *testing.T) {
	is := is.New(t)
	out, ds := apply(t, smttest.Registry(), sourceEnv, "transforms", "r", "transforms.r.type", smttest.Refuse)
	is.Equal(len(out.Processors), 0)
	is.Equal(entry(ds, "transforms.r.type").Reason, "Refuse without mode changes nothing.")
	is.Equal(entry(ds, "transforms.r.type").Target, (*kcmigrate.EntryTarget)(nil))
	is.Equal(out.Transforms[0].Status, kcmigrate.StatusTranslated)
}

func TestApply_Topic(t *testing.T) {
	is := is.New(t)
	env := sourceEnv
	env.Topic = "orders"
	out, _ := apply(t, smttest.Registry(), env, "transforms", "r1,r2", "transforms.r1.type", smttest.Route, "transforms.r2.type", smttest.Route)
	is.True(out.TopicChanged)
	is.Equal(out.Topic, `{{ print (print ("orders") "-routed") "-routed" }}`)

	env.Topic = `{{ index .Metadata "opencdc.collection" }}`
	out, _ = apply(t, smttest.Registry(), env, "transforms", "r", "transforms.r.type", smttest.Route)
	is.Equal(out.Topic, `{{ print (index .Metadata "opencdc.collection") "-routed" }}`)

	// Unknown topic: a topic-reading SMT refuses, and nothing is written.
	env.Topic = `prefix.{{ index .Metadata "opencdc.collection" }}`
	out, ds := apply(t, smttest.Registry(), env, "transforms", "r", "transforms.r.type", smttest.Route)
	is.True(!out.TopicChanged)
	is.Equal(got(entry(ds, "transforms.r.type")), refused)

	// A refused chain never changes the topic.
	env.Topic = "orders"
	out, _ = apply(t, smttest.Registry(), env, "transforms", "r,x", "transforms.r.type", smttest.Route, "transforms.x.type", "com.example.Custom")
	is.True(!out.TopicChanged)
}

func TestTopicPipeline(t *testing.T) {
	for in, want := range map[string]string{
		"orders": `"orders"`,
		`a"b`:    `"a\"b"`,
		`{{ index .Metadata "opencdc.collection" }}`: `index .Metadata "opencdc.collection"`,
		"":                     "",
		"p.{{ .Key }}":         "",
		"{{ .Key }}{{ .Key }}": "",
		"{{- .Key -}}":         "",
	} {
		got, ok := smt.TopicPipeline(in)
		if got != want || ok != (want != "") {
			t.Errorf("TopicPipeline(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestConditions(t *testing.T) {
	is := is.New(t)
	is.Equal(smt.And(), "")
	is.Equal(smt.And("", "a"), "a")
	is.Equal(smt.And("a", "b"), "and (a) (b)")
	is.Equal(smt.Not("a"), "not (a)")
	is.Equal(smt.Template(""), "")
	is.Equal(smt.NilGuard(smt.SideNone), "")
	is.Equal(smt.NilGuard(smt.SideKey), `ne (printf "%T" .Key) "<nil>"`)
}

// TestApply_TranslatorBugsFail: a translator result that breaks the
// framework's contract fails the run instead of reaching the report.
func TestApply_TranslatorBugsFail(t *testing.T) {
	bad := map[string]smt.Translator{
		"claims a missing option": func(*smt.Chain, *smt.SMT) (smt.Result, error) {
			return smt.Result{Processors: []smt.Processor{{Plugin: "filter"}}, Claimed: []string{"nope"}}, nil
		},
		"no processor, no reason": func(*smt.Chain, *smt.SMT) (smt.Result, error) { return smt.Result{}, nil },
		"no plugin": func(*smt.Chain, *smt.SMT) (smt.Result, error) {
			return smt.Result{Processors: []smt.Processor{{}}}, nil
		},
		"refusal with processors": func(*smt.Chain, *smt.SMT) (smt.Result, error) {
			return smt.Result{Processors: []smt.Processor{{Plugin: "filter"}}, Refusal: &smt.Refusal{Code: "kc.smt_unsupported", Reason: "r", Fix: "f"}}, nil
		},
		"refusal without fix": func(*smt.Chain, *smt.SMT) (smt.Result, error) {
			return smt.Result{Refusal: &smt.Refusal{Code: "kc.smt_unsupported", Reason: "r"}}, nil
		},
		"error": func(*smt.Chain, *smt.SMT) (smt.Result, error) { return smt.Result{}, fmt.Errorf("boom") },
	}
	for name, tr := range bad {
		t.Run(name, func(t *testing.T) {
			reg := &smt.Registry{Transforms: map[string]smt.Translator{"x.Bad": tr}}
			ds := decisions(t, "transforms", "a", "transforms.a.type", "x.Bad")
			if _, err := smt.Apply(data(t), reg, sourceEnv, ds); err == nil {
				t.Fatal("Apply accepted a broken translator result")
			}
		})
	}
}

// TestApply_UndecidedKeyFails: a claimed key the translator does not decide
// fails the run, so it can never fall off the report.
func TestApply_UndecidedKeyFails(t *testing.T) {
	ds := decisions(t, "tasks.max", "1")
	ds[0].Emit = &rules.Action{Kind: rules.SMTKind}
	if _, err := smt.Apply(data(t), smt.Builtin(), sourceEnv, ds); err == nil {
		t.Fatal("Apply accepted a claimed key it cannot decide")
	}
}

func TestApply_SecondUnwrapRefused(t *testing.T) {
	is := is.New(t)
	_, ds := apply(t, smttest.Registry(), sourceEnv, "transforms", "u1,u2", "transforms.u1.type", smttest.Unwrap, "transforms.u2.type", smttest.Unwrap)
	is.Equal(got(entry(ds, "transforms.u1.type")), refused) // refused with the chain
	is.True(strings.Contains(entry(ds, "transforms.u2.type").Reason, "second unwrap point"))
	_, ds = apply(t, smttest.Registry(), sourceEnv, "transforms", "u1", "transforms.u1.type", smttest.Unwrap)
	is.Equal(got(entry(ds, "transforms.u1.type")), ok)
}

// TestApply_LaterWriteRefusesTopicRead: the topic is evaluated against the
// final record, so an SMT whose topic reads what a later one writes is
// refused; the other order is fine.
func TestApply_LaterWriteRefusesTopicRead(t *testing.T) {
	is := is.New(t)
	env := sourceEnv
	env.Topic = "orders"
	out, ds := apply(t, smttest.Registry(), env, "transforms", "r,c", "transforms.r.type", smttest.Route, "transforms.c.type", smttest.Clobber)
	is.Equal(len(out.Processors), 0)
	is.True(strings.Contains(entry(ds, "transforms.r.type").Reason, "The transform c later in the chain writes"))
	out, _ = apply(t, smttest.Registry(), env, "transforms", "c,r", "transforms.c.type", smttest.Clobber, "transforms.r.type", smttest.Route)
	is.Equal(len(out.Processors), 1)
	is.True(out.TopicChanged)
}

func TestBothImages(t *testing.T) {
	is := is.New(t)
	ps := smt.BothImages(func(image string) smt.Processor {
		return smt.Processor{Plugin: "field.exclude", Settings: map[string]string{"fields": image + ".x"}}
	})
	is.Equal(ps, []smt.Processor{
		{Plugin: "field.exclude", Settings: map[string]string{"fields": ".Payload.After.x"}},
		{Plugin: "field.exclude", Settings: map[string]string{"fields": ".Payload.Before.x"}, Condition: `ne (printf "%T" .Payload.Before) "<nil>"`},
	})
}

// TestRegexRoute evaluates the RegexRouter topic pipeline the way the Kafka
// destination evaluates its topic template (text/template with sprig):
// Kafka Connect's matches() then replaceFirst().
func TestRegexRoute(t *testing.T) {
	for _, tt := range []struct{ pattern, repl, topic, want string }{
		{"a|ab", "X", "ab", "Xb"}, // replaceFirst finds the leftmost-first match, a proper prefix
		{`([^.]+)\.([^.]+)\.([^.]+)`, "${3}", "srv.public.users", "users"},
		{`(.*?)x`, "${1}y", "abx", "aby"},
		{"a+", "X", "baa", "baa"}, // no full match: unchanged
		{".*?", "X", "abc", "Xabc"},
	} {
		tmpl := smt.Template(smt.RegexRoute(tt.pattern, tt.repl, `index .Metadata "opencdc.collection"`))
		tp, err := template.New("topic").Funcs(sprig.FuncMap()).Parse(tmpl)
		if err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		var b bytes.Buffer
		if err := tp.Execute(&b, opencdc.Record{Metadata: opencdc.Metadata{"opencdc.collection": tt.topic}}); err != nil {
			t.Fatal(err)
		}
		if b.String() != tt.want {
			t.Errorf("RegexRoute(%q, %q) on %q = %q, want %q", tt.pattern, tt.repl, tt.topic, b.String(), tt.want)
		}
	}
}

// TestApply_SchemaExcludePerCondition: a record that skips the first
// reshaping processor (its predicate is false) but matches a later one
// must lose its subject too, so each distinct condition gets its own
// exclude; a key-side reshape excludes the key's subject and version.
func TestApply_SchemaExcludePerCondition(t *testing.T) {
	is := is.New(t)
	out, _ := apply(t, smttest.Registry(), sourceEnv,
		"transforms", "s1,s2,s3",
		"transforms.s1.type", smttest.Split, "transforms.s1.a", "x", "transforms.s1.predicate", "p",
		"transforms.s2.type", smttest.Split, "transforms.s2.a", "y",
		"transforms.s3.type", smttest.Split, "transforms.s3.a", "z", // same condition as s2: no third exclude
		"predicates", "p", "predicates.p.type", smttest.Always)
	var ids []string
	for _, p := range out.Processors {
		ids = append(ids, p.ID)
	}
	is.Equal(ids, []string{"smt-schema-value", "smt-s1", "smt-schema-value-2", "smt-s2", "smt-s3"})
	is.Equal(out.Processors[0].Condition, out.Processors[1].Condition)
	is.Equal(out.Processors[2].Condition, out.Processors[3].Condition)
	is.True(out.Processors[0].Condition != out.Processors[2].Condition)

	out, _ = apply(t, smttest.Registry(), sourceEnv, "transforms", "k", "transforms.k.type", smttest.Split+"$Key", "transforms.k.a", "id")
	is.Equal(out.Processors[0].ID, "smt-schema-key")
	is.Equal(out.Processors[0].Settings["fields"], `.Metadata["opencdc.key.schema.subject"],.Metadata["opencdc.key.schema.version"]`)
	is.Equal(out.Processors[1].Settings["fields"], ".Key.id")
}

// TestRefusalTexts_NameNoUnshippedRelease: no refusal promises v0.22, a
// release whose translators may miss the cut (design decision 21).
func TestRefusalTexts_NameNoUnshippedRelease(t *testing.T) {
	for _, class := range []string{
		"org.apache.kafka.connect.transforms.Filter", "org.apache.kafka.connect.transforms.InsertField$Value",
		"org.apache.kafka.connect.transforms.predicates.TopicNameMatches", "io.debezium.transforms.ExtractNewRecordState",
	} {
		_, ds := apply(t, smt.Builtin(), sourceEnv, "transforms", "t", "transforms.t.type", class)
		if e := entry(ds, "transforms.t.type"); strings.Contains(e.Reason+e.Fix, "v0.22") {
			t.Errorf("%s: refusal names v0.22: %s %s", class, e.Reason, e.Fix)
		}
	}
}

// TestApply_ReferenceWhereUnused: Kafka Connect resolves a config-provider
// reference even under an unlisted alias or an unused predicate, and fails
// the connector when it cannot; migrate cannot read it, so the key blocks
// and the chain is refused with it.
func TestApply_ReferenceWhereUnused(t *testing.T) {
	for name, kvs := range map[string][]string{
		"unlisted alias":   {"transforms", "a", "transforms.a.type", smttest.Drop, "transforms.z.x", "${env:X}"},
		"unused predicate": {"transforms", "a", "transforms.a.type", smttest.Drop, "predicates", "p", "predicates.p.type", smttest.Always, "predicates.p.x", "${env:X}"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			out, ds := apply(t, smttest.Registry(), sourceEnv, kvs...)
			is.Equal(len(out.Processors), 0)
			is.Equal(got(entry(ds, kvs[len(kvs)-2])), refused)
			is.Equal(got(entry(ds, "transforms")), refused)
		})
	}
}
