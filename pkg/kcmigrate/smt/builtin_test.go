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
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
	"github.com/matryer/is"
)

// The translators' behaviour on data is pinned by the oracle replay
// (tests/migrate/smt-oracle/replay/translators_test.go); these tests pin
// what they emit and every refusal path.

const (
	kc   = "org.apache.kafka.connect.transforms."
	kcp  = kc + "predicates."
	enrs = "io.debezium.transforms.ExtractNewRecordState"
	off  = "false"
)

// debeziumEnv is a Debezium Postgres source chain with a known topic.
var debeziumEnv = smt.Env{
	Scope: rules.ScopeSource, Source: "builtin:postgres", Destination: "builtin:kafka",
	ProcessorPath: "/pipelines/0/connectors/0/processors",
	SchemaMode:    true, KeyShape: smt.ShapeStructured, ValueShape: smt.ShapeStructured,
	Topic: `{{ print "dbz.public." (index .Metadata "opencdc.collection") }}`,
}

// s3Env is an S3 sink chain after the Kafka source.
var s3Env = smt.Env{
	Scope: rules.ScopeSink, Source: "builtin:kafka", Destination: "builtin:s3",
	ProcessorPath: "/pipelines/0/connectors/1/processors",
	KeyShape:      smt.ShapeRaw, ValueShape: smt.ShapeRaw,
	Topic: `{{ index .Metadata "opencdc.collection" }}`,
}

func run(t *testing.T, env smt.Env, kvs ...string) (smt.Outcome, []rules.Decision) {
	t.Helper()
	return apply(t, smt.Builtin(), env, kvs...)
}

// unwrap is ExtractNewRecordState in its default mode, translatable.
var unwrap = []string{"transforms.u.type", enrs, "transforms.u.replace.null.with.default", off}

func with(chain string, kvs ...[]string) []string {
	out := []string{"transforms", chain}
	for _, k := range kvs {
		out = append(out, k...)
	}
	return out
}

func conditionTrue(t *testing.T, cond string, r opencdc.Record) bool {
	t.Helper()
	tp, err := template.New("c").Funcs(sprig.FuncMap()).Parse(cond)
	if err != nil {
		t.Fatalf("condition %q: %v", cond, err)
	}
	var b strings.Builder
	if err := tp.Execute(&b, r); err != nil {
		t.Fatalf("condition %q: %v", cond, err)
	}
	return b.String() == "true"
}

func TestFilter(t *testing.T) {
	is := is.New(t)
	out, ds := run(t, debeziumEnv, with("f", []string{"transforms.f.type", kc + "Filter"})...)
	is.Equal(got(entry(ds, "transforms.f.type")), ok)
	is.Equal(len(out.Processors), 1)
	is.Equal(out.Processors[0].Plugin, "filter")
	is.Equal(out.Processors[0].Condition, "") // no predicate: Kafka Connect drops every record

	_, ds = run(t, debeziumEnv, with("f", []string{"transforms.f.type", kc + "Filter$Value"})...)
	is.True(strings.Contains(entry(ds, "transforms.f.type").Reason, "has no class"))
}

// TestRecordIsTombstone_ByPosition: one predicate named before and after
// the unwrap is translated at each position: nothing is a tombstone before
// it (Debezium's own tombstones have no native counterpart), a delete is
// after it, and nothing after drop mode.
func TestRecordIsTombstone_ByPosition(t *testing.T) {
	is := is.New(t)
	out, ds := run(t, debeziumEnv, with("a,u,b",
		[]string{"transforms.a.type", kc + "Filter", "transforms.a.predicate", "t"},
		unwrap,
		[]string{"transforms.b.type", kc + "Filter", "transforms.b.predicate", "t", "transforms.b.negate", "true"},
		[]string{"predicates", "t", "predicates.t.type", kcp + "RecordIsTombstone"})...)
	is.Equal(got(entry(ds, "transforms")), ok)
	is.Equal(len(out.Processors), 2)
	is.Equal(out.Processors[0].Condition, "{{ false }}")
	is.Equal(out.Processors[1].Condition, `{{ not (eq (printf "%v" .Operation) "delete") }}`)

	out, _ = run(t, debeziumEnv, with("u,b",
		[]string{"transforms.u.type", enrs, "transforms.u.replace.null.with.default", off, "transforms.u.delete.tombstone.handling.mode", "drop"},
		[]string{"transforms.b.type", kc + "Filter", "transforms.b.predicate", "t"},
		[]string{"predicates", "t", "predicates.t.type", kcp + "RecordIsTombstone"})...)
	is.Equal(len(out.Processors), 2)
	is.Equal(out.Processors[0].Condition, `{{ eq (printf "%v" .Operation) "delete" }}`) // drop mode
	is.Equal(out.Processors[1].Condition, "{{ false }}")

	_, ds = run(t, s3Env, with("f",
		[]string{"transforms.f.type", kc + "Filter", "transforms.f.predicate", "t"},
		[]string{"predicates", "t", "predicates.t.type", kcp + "RecordIsTombstone"})...)
	is.Equal(entry(ds, "predicates.t.type").Code, kcmigrate.CodeTargetCapabilityMissing.Reason())
}

// TestTopicNameMatches_AfterRouter: the topic a predicate reads is the one
// at its position, after a RegexRouter.
func TestTopicNameMatches_AfterRouter(t *testing.T) {
	is := is.New(t)
	out, ds := run(t, debeziumEnv, with("f1,r,f2",
		[]string{"transforms.f1.type", kc + "Filter", "transforms.f1.predicate", "p"},
		[]string{"transforms.r.type", kc + "RegexRouter", "transforms.r.regex", `dbz\.public\.(.*)`, "transforms.r.replacement", "cdc.$1"},
		[]string{"transforms.f2.type", kc + "Filter", "transforms.f2.predicate", "p"},
		[]string{"predicates", "p", "predicates.p.type", kcp + "TopicNameMatches", "predicates.p.pattern", `cdc\.items`})...)
	is.Equal(got(entry(ds, "transforms")), ok)
	rec := opencdc.Record{Metadata: opencdc.Metadata{"opencdc.collection": "items"}}
	is.True(!conditionTrue(t, out.Processors[0].Condition, rec)) // dbz.public.items
	is.True(conditionTrue(t, out.Processors[1].Condition, rec))  // cdc.items
	is.True(out.TopicChanged)
}

func TestRegexRouter(t *testing.T) {
	route := []string{"transforms.r.type", kc + "RegexRouter", "transforms.r.regex", `dbz\.public\.(a|ab)`, "transforms.r.replacement", "x-$1"}
	t.Run("translated", func(t *testing.T) {
		is := is.New(t)
		out, ds := run(t, debeziumEnv, with("r", route)...)
		is.Equal(got(entry(ds, "transforms.r.type")), ok)
		is.Equal(len(out.Processors), 0)
		is.True(out.TopicChanged)
		is.True(strings.Contains(out.Topic, "`x-${1}`"))
	})
	t.Run("predicated", func(t *testing.T) {
		is := is.New(t)
		out, ds := run(t, debeziumEnv, with("r", route,
			[]string{"transforms.r.predicate", "p", "predicates", "p", "predicates.p.type", kcp + "TopicNameMatches", "predicates.p.pattern", "dbz.*"})...)
		is.Equal(got(entry(ds, "transforms.r.predicate")), ok)
		is.True(strings.HasPrefix(out.Topic, "{{ ternary ("))
	})
	refusals := []struct {
		name string
		env  smt.Env
		kvs  []string
		key  string
		want string
	}{
		{"topic unknown", func() smt.Env { e := debeziumEnv; e.Topic = ""; return e }(), route, "transforms.r.type", "does not know the Kafka Connect topic"},
		{"no topic setting", s3Env, route, "transforms.r.type", "no setting that names a Kafka topic"},
		{"java construct", debeziumEnv, []string{"transforms.r.type", kc + "RegexRouter", "transforms.r.regex", `(?=a)b`, "transforms.r.replacement", "x"}, "transforms.r.regex", "not translated"},
		{"missing group", debeziumEnv, []string{"transforms.r.type", kc + "RegexRouter", "transforms.r.regex", `(a)`, "transforms.r.replacement", "$2"}, "transforms.r.replacement", "does not have"},
		{"no regex", debeziumEnv, []string{"transforms.r.type", kc + "RegexRouter", "transforms.r.replacement", "x"}, "transforms.r.type", "regex is not set"},
		{"suffix", debeziumEnv, []string{"transforms.r.type", kc + "RegexRouter$Value", "transforms.r.regex", "a", "transforms.r.replacement", "x"}, "transforms.r.type", "has no class"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			out, ds := run(t, tt.env, with("r", tt.kvs)...)
			e := entry(ds, tt.key)
			is.Equal(e.Status, kcmigrate.StatusUnsupported)
			is.True(e.Blocking)
			is.True(strings.Contains(e.Reason, tt.want)) // reason
			is.True(!out.TopicChanged)
		})
	}
}

func TestReplaceField(t *testing.T) {
	t.Run("exclude and rename", func(t *testing.T) {
		is := is.New(t)
		out, ds := run(t, debeziumEnv, with("u,r", unwrap, []string{
			"transforms.r.type", kc + "ReplaceField$Value", "transforms.r.exclude", "big, attrs",
			"transforms.r.renames", "note:remark", "transforms.r.replace.null.with.default", off,
		})...)
		is.Equal(got(entry(ds, "transforms")), ok)
		var plugins []string
		for _, p := range out.Processors {
			plugins = append(plugins, p.ID+":"+p.Plugin)
		}
		is.Equal(plugins, []string{
			"smt-schema-value:field.exclude", "smt-r-1:field.exclude", // After, under its nil guard
			"smt-schema-value-2:field.exclude", "smt-r-2:field.exclude", // Before, nil-guarded
			"smt-schema-value-3:field.exclude", // decoded before the guards read fields (NeedsDecoded)
			"smt-r-3:error", "smt-r-4:field.rename", "smt-r-5:field.rename",
		})
		is.Equal(out.Processors[1].Settings["fields"], ".Payload.After.big,.Payload.After.attrs")
		is.Equal(out.Processors[3].Settings["fields"], ".Payload.Before.big,.Payload.Before.attrs")
		is.Equal(out.Processors[4].Condition, "") // no predicate: the schema keys go for every record
		is.Equal(out.Processors[6].Settings["mapping"], ".Payload.After.note:remark")
		is.Equal(entry(ds, "transforms.r.renames").Target.ConfigPath, out.Processors[5].Path)

		after := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"note": nil, "remark": 1}}}
		is.True(conditionTrue(t, out.Processors[5].Condition, after)) // the target exists: fail, as Kafka Connect does
		del := opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData{"note": "x", "remark": 1}}}
		is.True(!conditionTrue(t, out.Processors[5].Condition, del))
		is.True(!conditionTrue(t, out.Processors[7].Condition, del)) // a delete's before-image never fails
		is.True(conditionTrue(t, out.Processors[7].Condition, opencdc.Record{Payload: opencdc.Change{Before: opencdc.StructuredData{"note": nil}}}))
		is.True(!conditionTrue(t, out.Processors[7].Condition, opencdc.Record{}))
	})
	base := []string{"transforms.r.type", kc + "ReplaceField$Value", "transforms.r.replace.null.with.default", off}
	refusals := []struct {
		name string
		env  smt.Env
		pre  bool // after an unwrap
		kvs  []string
		key  string
		code string
		want string
	}{
		{"envelope", debeziumEnv, false, []string{"transforms.r.exclude", "source"}, "transforms.r.type", "kc.smt_unsupported", "envelope"},
		{"include", debeziumEnv, true, []string{"transforms.r.include", "a"}, "transforms.r.include", "kc.smt_unsupported", "v0.23"},
		{"swap", debeziumEnv, true, []string{"transforms.r.renames", "a:b,b:a"}, "transforms.r.renames", "kc.smt_unsupported", "both a rename target and a rename source"},
		{"chain", debeziumEnv, true, []string{"transforms.r.renames", "a:b,b:c"}, "transforms.r.renames", "kc.smt_unsupported", "both a rename target and a rename source"},
		{"excluded and renamed", debeziumEnv, true, []string{"transforms.r.renames", "a:b", "transforms.r.exclude", "a"}, "transforms.r.renames", "kc.smt_unsupported", "both excluded and renamed"},
		{"two onto one", debeziumEnv, true, []string{"transforms.r.renames", "a:c,b:c"}, "transforms.r.renames", "kc.smt_unsupported", "Two fields"},
		{"bad mapping", debeziumEnv, true, []string{"transforms.r.renames", "a"}, "transforms.r.renames", "kc.smt_unsupported", "not old:new"},
		{"dotted name", debeziumEnv, true, []string{"transforms.r.exclude", "a.b"}, "transforms.r.exclude", "kc.smt_unsupported", "letters, digits and underscores"},
		{"bytes", s3Env, false, []string{"transforms.r.exclude", "a"}, "transforms.r.type", "kc.smt_unsupported", "raw bytes"},
		{"schema", func() smt.Env { e := debeziumEnv; e.ValueSchema = true; return e }(), true, []string{"transforms.r.exclude", "a"}, "transforms.r.type", "kc.smt_unsupported", "schema-carrying converter"},
		{"key", debeziumEnv, true, []string{"transforms.r.type", kc + "ReplaceField$Key", "transforms.r.exclude", "a"}, "transforms.r.type", "kc.smt_unsupported", "v0.23"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			kvs := append(append([]string{}, base...), tt.kvs...)
			chain, links := "r", [][]string{kvs}
			if tt.pre {
				chain, links = "u,r", [][]string{unwrap, kvs}
			}
			out, ds := run(t, tt.env, with(chain, links...)...)
			e := entry(ds, tt.key)
			is.Equal(got(e), want{kcmigrate.StatusUnsupported, tt.code, true})
			is.True(strings.Contains(e.Reason, tt.want)) // reason
			is.Equal(len(out.Processors), 0)
		})
	}
	t.Run("default replacement on", func(t *testing.T) {
		is := is.New(t)
		_, ds := run(t, debeziumEnv, with("u,r", unwrap, []string{"transforms.r.type", kc + "ReplaceField$Value", "transforms.r.exclude", "a"})...)
		is.Equal(got(entry(ds, "transforms.r.type")), want{kcmigrate.StatusUnsupported, "kc.setting_required", true})
		_, ds = run(t, debeziumEnv, with("u,r", unwrap, []string{"transforms.r.type", kc + "ReplaceField$Value", "transforms.r.exclude", "a", "transforms.r.replace.null.with.default", "TRUE"})...)
		is.Equal(got(entry(ds, "transforms.r.replace.null.with.default")), want{kcmigrate.StatusUnsupported, "kc.setting_required", true})
	})
}

func TestExtractNewRecordState(t *testing.T) {
	t.Run("tombstone mode", func(t *testing.T) {
		is := is.New(t)
		out, ds := run(t, debeziumEnv, with("u", unwrap)...)
		is.Equal(got(entry(ds, "transforms.u.type")), ok)
		is.Equal(len(out.Processors), 0)
		is.True(strings.Contains(entry(ds, "transforms.u.type").Reason, "No processor"))
	})
	refusals := []struct {
		name string
		env  smt.Env
		kvs  []string
		key  string
		code string
	}{
		{"option unset", debeziumEnv, []string{"transforms.u.type", enrs}, "transforms.u.type", "kc.setting_required"},
		{"rewrite", debeziumEnv, append(append([]string{}, unwrap...), "transforms.u.delete.tombstone.handling.mode", "rewrite"), "transforms.u.delete.tombstone.handling.mode", "kc.smt_unsupported"},
		{"add.fields", debeziumEnv, append(append([]string{}, unwrap...), "transforms.u.add.fields", "op"), "transforms.u.add.fields", "kc.smt_unsupported"},
		{"removed option", debeziumEnv, append(append([]string{}, unwrap...), "transforms.u.drop.tombstones", "true"), "transforms.u.drop.tombstones", "kc.smt_unsupported"},
		{"unknown option", debeziumEnv, append(append([]string{}, unwrap...), "transforms.u.nope", "1"), "transforms.u.nope", "kc.key_unknown"},
		{"sink", s3Env, unwrap, "transforms.u.type", "kc.smt_unsupported"},
		{"predicate", debeziumEnv, append(append([]string{}, unwrap...), "transforms.u.predicate", "p", "predicates", "p", "predicates.p.type", kcp+"RecordIsTombstone"), "transforms.u.type", "kc.smt_unsupported"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			_, ds := run(t, tt.env, with("u", tt.kvs)...)
			is.Equal(got(entry(ds, tt.key)), want{kcmigrate.StatusUnsupported, tt.code, true})
		})
	}
}

func TestExtractField(t *testing.T) {
	is := is.New(t)
	after := []string{"transforms.x.type", kc + "ExtractField$Value", "transforms.x.field", "after", "transforms.x.replace.null.with.default", off}
	out, ds := run(t, debeziumEnv, with("x", after)...)
	is.Equal(got(entry(ds, "transforms.x.type")), ok)
	is.Equal(len(out.Processors), 0)

	for name, kvs := range map[string][]string{
		"other field": {"transforms.x.type", kc + "ExtractField$Value", "transforms.x.field", "before", "transforms.x.replace.null.with.default", off},
		"key":         {"transforms.x.type", kc + "ExtractField$Key", "transforms.x.field", "id"},
		"no suffix":   {"transforms.x.type", kc + "ExtractField", "transforms.x.field", "after"},
	} {
		_, ds := run(t, debeziumEnv, with("x", kvs)...)
		if entry(ds, "transforms.x.type").Status != kcmigrate.StatusUnsupported {
			t.Errorf("%s: translated", name)
		}
	}
	// A second unwrap point is refused by the chain.
	_, ds = run(t, debeziumEnv, with("u,x", unwrap, after)...)
	is.Equal(entry(ds, "transforms.x.type").Status, kcmigrate.StatusUnsupported)
}

// TestHasHeaderKey: on a sink after the Kafka source, the condition is true
// exactly when the metadata has kafka.header.<name>, whatever the values.
func TestHasHeaderKey(t *testing.T) {
	is := is.New(t)
	out, ds := run(t, s3Env, with("f",
		[]string{"transforms.f.type", kc + "Filter", "transforms.f.predicate", "h"},
		[]string{"predicates", "h", "predicates.h.type", kcp + "HasHeaderKey", "predicates.h.name", `x"trace`})...)
	is.Equal(got(entry(ds, "predicates.h.name")), ok)
	cond := out.Processors[0].Condition
	for md, want := range map[*opencdc.Metadata]bool{
		{`kafka.header.x"trace`: ""}:                                     true,
		{`kafka.header.x"trace`: "v", "other": "x"}:                      true,
		{"kafka.header.x": `"trace`}:                                     false,
		{"a": `kafka.header.x"trace":`, "b": `"kafka.header.x\"trace":`}: false,
		{"kafka.header.X\"trace": ""}:                                    false,
		{}:                                                               false,
	} {
		is.Equal(conditionTrue(t, cond, opencdc.Record{Metadata: *md}), want) // metadata
	}

	for name, env := range map[string]smt.Env{
		"source": debeziumEnv,
		"mirror": func() smt.Env { e := s3Env; e.MirrorMetadata = true; return e }(),
	} {
		_, ds := run(t, env, with("f",
			[]string{"transforms.f.type", kc + "Filter", "transforms.f.predicate", "h"},
			[]string{"predicates", "h", "predicates.h.type", kcp + "HasHeaderKey", "predicates.h.name", "x"})...)
		if entry(ds, "predicates.h.type").Status != kcmigrate.StatusUnsupported {
			t.Errorf("%s: translated", name)
		}
	}
}

// TestEnvRefusal: a refusal of the connector refuses every SMT with a
// translator; a class without one keeps its own reason.
func TestEnvRefusal(t *testing.T) {
	is := is.New(t)
	env := debeziumEnv
	env.Refusal = &smt.Refusal{Code: kcmigrate.CodeSettingRequired.Reason(), Reason: "converters", Fix: "set them"}
	_, ds := run(t, env, with("f,c",
		[]string{"transforms.f.type", kc + "Filter"},
		[]string{"transforms.c.type", kc + "Cast$Value"})...)
	is.Equal(got(entry(ds, "transforms.f.type")), want{kcmigrate.StatusUnsupported, "kc.setting_required", true})
	is.Equal(entry(ds, "transforms.f.type").Reason, "converters")
	is.True(strings.Contains(entry(ds, "transforms.c.type").Reason, "v0.23"))
}

// TestOptionValue_JavaTrim: option values are trimmed as Java's
// String.trim does: control characters and spaces go, U+00A0 stays (and
// then makes the pattern non-ASCII, so it is refused).
func TestOptionValue_JavaTrim(t *testing.T) {
	is := is.New(t)
	filter := []string{"transforms.f.type", kc + "Filter", "transforms.f.predicate", "p"}
	out, ds := run(t, debeziumEnv, with("f", filter,
		[]string{"predicates", "p", "predicates.p.type", kcp + "TopicNameMatches", "predicates.p.pattern", "\t\x0bdbz.*\r\n "})...)
	is.Equal(got(entry(ds, "predicates.p.pattern")), ok)
	is.True(strings.Contains(out.Processors[0].Condition, "`^(?:dbz.*)$`"))

	_, ds = run(t, debeziumEnv, with("f", filter,
		[]string{"predicates", "p", "predicates.p.type", kcp + "TopicNameMatches", "predicates.p.pattern", "dbz.* "})...)
	is.True(strings.Contains(entry(ds, "predicates.p.pattern").Reason, "printable ASCII"))
}
