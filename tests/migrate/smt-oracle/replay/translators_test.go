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

package replay

// The translator gate (SMT pack design decision 21): every oracle chain is
// migrated with the shipped translators, and a chain that translates is
// replayed through the processors migrate emitted, over the committed
// input records, and must diff clean against the committed Kafka Connect
// output. A chain that is not in the translated lists below must be
// refused, so no SMT ships without its diff.

import (
	"bytes"
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-kafka/destination"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	provyaml "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
	"github.com/goccy/go-json"
)

// debeziumBase is the Debezium Postgres connector of the oracle capture
// (capture.sh): topic prefix dbz, the fixture tables in schema public, and
// JsonConverters that keep NULLs (design decision 22).
var debeziumBase = map[string]string{
	"connector.class":                           "io.debezium.connector.postgresql.PostgresConnector",
	"database.hostname":                         "postgres.internal.example",
	"database.port":                             "5432",
	"database.user":                             "postgres",
	"database.password":                         "PLACEHOLDER",
	"database.dbname":                           "smt",
	"plugin.name":                               "pgoutput",
	"topic.prefix":                              "dbz",
	"table.include.list":                        "public.items,public.a,public.ab,public.strict,public.toast,public.noschema",
	"key.converter":                             "org.apache.kafka.connect.json.JsonConverter",
	"key.converter.schemas.enable":              "false",
	"key.converter.replace.null.with.default":   "false",
	"value.converter":                           "org.apache.kafka.connect.json.JsonConverter",
	"value.converter.schemas.enable":            "false",
	"value.converter.replace.null.with.default": "false",
}

// s3Base is an S3 sink reading raw bytes, whose chain runs on the
// destination after the Kafka source.
var s3Base = map[string]string{
	"connector.class": "io.confluent.connect.s3.S3SinkConnector",
	"topics":          "t",
	"s3.bucket.name":  "example-b",
	"key.converter":   "org.apache.kafka.connect.converters.ByteArrayConverter",
	"value.converter": "org.apache.kafka.connect.converters.ByteArrayConverter",
}

// debeziumTranslated are the chains over the real Debezium capture that
// translate, with whether Kafka Connect keeps Debezium's own tombstone
// after a delete. The native source emits no tombstone (the
// kc.output_shape_differs note covers it), so a kept one is that known
// difference, checked against the golden, and a dropped one needs nothing.
var debeziumTranslated = map[string]map[string]bool{
	"pg-unwrap": {
		"defaults-enrs-null-false":                          false,
		"delete-drop-enrs-null-false":                       false,
		"default-then-drop-tombstones-enrs-null-false":      false,
		"drop-mode-then-drop-tombstones-enrs-null-false":    false,
		"default-then-keep-tombstones-only-enrs-null-false": false,
		"topic-predicate-before-unwrap-enrs-null-false":     false,
		"tombstone-filter-before-unwrap-enrs-null-false":    false,
		"router-predicated-enrs-null-false":                 false,
		"router-then-topic-filter-enrs-null-false":          false,
	},
	"pg-reshape": {
		"exclude":                              false,
		"exclude-rename-enrs-null-false":       false,
		"rename-defaulted-column-false":        false,
		"rename-onto-existing-enrs-null-false": false,
		"regexrouter-alternation":              false,
		"regexrouter-lazy":                     false,
		"regexrouter-table-to-topic":           false,
	},
	"pg-envelope": {
		"extract-after":                           true,
		"extract-after-then-drop-tombstones":      false,
		"extract-after-then-keep-tombstones-only": true,
	},
}

// debeziumErrors pairs the Kafka Connect failures of translated chains
// with the Conduit error the translation fails the record with.
var debeziumErrors = []ErrorMatch{
	// ReplaceField onto an existing field: a duplicate field, or the reverse
	// lookup of the target finding no source.
	{KCClass: "DataException", Conduit: "is already a field of the record"},
	{KCClass: "SchemaBuilderException", KCMessage: "field name duplication", Conduit: "is already a field of the record"},
}

// TestTranslators_GuardsNeedTheDecodedRow is the negative control for
// Processor.NeedsDecoded: the Postgres source attaches a schema, so the
// row reaches the chain as encoded bytes and a condition that tests a field
// of it is false until a processor has decoded it and dropped the schema
// keys. Without the schema-key removal before the rename guards, the
// renames are skipped and the replay must report the difference.
func TestTranslators_GuardsNeedTheDecodedRow(t *testing.T) {
	cases, err := smtoracle.LoadCases("pg-reshape")
	if err != nil {
		t.Fatal(err)
	}
	var run smtoracle.InputRun
	for _, r := range cases.Runs {
		if r.Run == "rename-defaulted-column-false" {
			run = r
		}
	}
	_, pipe := migrateChain(t, debeziumBase, run)
	steps, _ := chainOf(t, pipe, 0)
	var without []Step
	for _, st := range steps {
		if !strings.Contains(st.Settings["fields"], "schema.subject") {
			without = append(without, st)
		}
	}
	if len(without) == len(steps) {
		t.Fatal("the chain has no schema-key removal to leave out")
	}
	suite, err := smtoracle.Load("pg-reshape")
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := smtoracle.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := Records()
	if err != nil {
		t.Fatal(err)
	}
	outs, err := newReplayer(t).Run(context.Background(), without, recs)
	if err != nil {
		t.Fatal(err)
	}
	renamedLost := 0
	for _, p := range pairs {
		if p.Table != TableItems || p.Op == "delete" {
			continue
		}
		cd, err := FromConduit(outs[p.Conduit], Mapping{})
		if err != nil {
			t.Fatal(err)
		}
		diffs, err := Compare(kcView(t, suite, run.Run, p.Debezium), cd, PostgresOptions(p.Table))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range diffs {
			if strings.Contains(d, "value.remark: only in Kafka Connect") {
				renamedLost++
			}
		}
	}
	if renamedLost == 0 {
		t.Fatal("without the schema-key removal the rename guards still saw the row; the control no longer exercises NeedsDecoded")
	}
}

// TestTranslators_DebeziumChains is the gate for the Debezium source
// chains: each oracle chain is migrated; a translated one must diff clean
// over every captured change, and every other one must be refused.
func TestTranslators_DebeziumChains(t *testing.T) { //nolint:gocyclo // one pass per oracle suite and run, with the gate's cases spelled out
	pairs, err := smtoracle.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := Records()
	if err != nil {
		t.Fatal(err)
	}
	for suiteName, translated := range debeziumTranslated {
		cases, err := smtoracle.LoadCases(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		suite, err := smtoracle.Load(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, run := range cases.Runs {
			seen[run.Run] = true
			t.Run(suiteName+"/"+run.Run, func(t *testing.T) {
				kept, want := translated[run.Run]
				res, pipe := migrateChain(t, debeziumBase, run)
				ok := chainTranslated(t, res)
				switch {
				case want && !ok:
					t.Fatalf("expected the chain to translate, it was refused: %s", refusalReasons(res))
				case !want && ok:
					t.Fatalf("the chain translates but has no oracle diff here; add it to debeziumTranslated so it is replayed")
				case !want:
					return // refused, as it must be
				}
				steps, topic := chainOf(t, pipe, 0)
				outs, err := newReplayer(t).Run(context.Background(), steps, recs)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range pairs {
					kc := kcView(t, suite, run.Run, p.Debezium)
					cd, err := FromConduit(outs[p.Conduit], Mapping{Topic: evalTopic(t, topic, outs[p.Conduit].Record)})
					if err != nil {
						t.Fatal(err)
					}
					opts := PostgresOptions(p.Table)
					opts.ErrorMatch = debeziumErrors
					diffs, err := Compare(kc, cd, opts)
					if err != nil {
						t.Fatal(err)
					}
					for _, d := range diffs {
						t.Errorf("%s %s: %s", p.Debezium, p.Op, d)
					}
					if p.Tombstone != "" {
						for _, d := range CompareTombstone(kcView(t, suite, run.Run, p.Tombstone), kept) {
							t.Errorf("%s: %s", p.Tombstone, d)
						}
					}
				}
			})
		}
		for name := range translated {
			if !seen[name] {
				t.Errorf("%s: no oracle run %q", suiteName, name)
			}
		}
	}
}

// TestTranslators_FilterOnS3Sink is the gate for sink chains: Filter with
// TopicNameMatches and HasHeaderKey on an S3 sink, whose chain runs after
// the Kafka source. The Conduit records are built from the oracle's input
// records as the Kafka source (v0.17.0, not mirror mode) reads them: the
// topic in opencdc.collection, each header as kafka.header.<name> (a null
// value as ""), raw key and value. Filters do not touch the key or value,
// so the outcome and the topic are compared.
func TestTranslators_FilterOnS3Sink(t *testing.T) { //nolint:gocyclo // one pass per oracle suite and run, with the gate's cases spelled out
	translated := map[string]map[string]bool{
		"filter-predicates": {
			"filter-no-predicate": true, "topic-matches": true, "topic-matches-negate": true,
			"topic-matches-bare-pattern": true, "topic-matches-alternation": true,
			"has-header-key": true, "has-header-key-negate": true,
		},
		"regexrouter-unicode": {"predicate-ci": true, "predicate-alt": true},
	}
	// Kafka accepts only letters, digits, '.', '_' and '-' in a topic name,
	// which the regex translation relies on (regex.go). The oracle's
	// hand-written records include names Kafka rejects; on those RE2's (?i)
	// also folds the Kelvin sign, which Java does not. They are listed here,
	// so the set cannot grow unnoticed.
	knownIllegalTopic := map[string]bool{"regexrouter-unicode/predicate-ci/kelvin": true}

	for suiteName, runs := range translated {
		cases, err := smtoracle.LoadCases(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		suite, err := smtoracle.Load(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range cases.Runs {
			if !isFilterChain(run) {
				continue // the router runs are checked in TestTranslators_RegexRouterTopics
			}
			t.Run(suiteName+"/"+run.Run, func(t *testing.T) {
				res, pipe := migrateChain(t, s3Base, run)
				ok := chainTranslated(t, res)
				switch {
				case runs[run.Run] && !ok:
					t.Fatalf("expected the chain to translate, it was refused: %s", refusalReasons(res))
				case !runs[run.Run] && ok:
					t.Fatal("the chain translates but is not in the translated list")
				case !ok:
					return
				}
				steps, _ := chainOf(t, pipe, 1)
				inputs, err := cases.RecordsFor(run.Run)
				if err != nil {
					t.Fatal(err)
				}
				recs := make([]opencdc.Record, len(inputs))
				for i, in := range inputs {
					recs[i] = kafkaSourceRecord(t, in)
				}
				r, err := New(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				outs, err := r.Run(context.Background(), steps, recs)
				if err != nil {
					t.Fatal(err)
				}
				for i, in := range inputs {
					res, ok := suite.Find(run.Run, in.Name)
					if !ok {
						t.Fatalf("no golden for %s", in.Name)
					}
					got := ViewRecord
					switch {
					case outs[i].Err != nil:
						got = ViewError
					case outs[i].Filtered:
						got = ViewDropped
					}
					differs := got != res.Result || (got == ViewRecord && res.Output.Topic != in.Topic)
					key := suiteName + "/" + run.Run + "/" + in.Name
					switch {
					case differs && knownIllegalTopic[key]:
						t.Logf("%s: known difference on a topic name Kafka rejects (%q)", in.Name, in.Topic)
					case differs:
						t.Errorf("%s: Kafka Connect %s, Conduit %s", in.Name, res.Result, got)
					case knownIllegalTopic[key]:
						t.Errorf("%s: listed as a known difference but agrees; remove it from the list", in.Name)
					}
				}
			})
		}
	}
}

// TestTranslators_RegexRouterTopics checks the RegexRouter translation
// against every hand-written router golden. No connector class of this
// version runs a router over Kafka topic names (the S3 sink has no topic
// setting, MirrorMaker's topic is v0.23), so the chain is translated for a
// Kafka-to-Kafka sink environment directly, and the topic template it ends
// with is evaluated by the Kafka destination's own code on each record.
func TestTranslators_RegexRouterTopics(t *testing.T) { //nolint:gocyclo // one pass per oracle suite and run, with the gate's cases spelled out
	refused := map[string]string{
		"regexrouter/missing-group": "refers to a group the pattern does not have",
		// Java's (?u) turns Unicode case folding on; RE2 has no such flag.
		"regexrouter-unicode/ci-unicode-case": "is not translated",
		// A non-ASCII literal: RE2's (?i) would fold it onto ASCII.
		"regexrouter-unicode/ci-kelvin-literal": "not printable ASCII",
	}
	knownIllegalTopic := map[string]bool{
		// (?i)k: RE2 also matches the Kelvin sign, Java does not; Kafka
		// rejects that topic name.
		"regexrouter-unicode/ci-ascii/kelvin": true,
	}
	env := smt.Env{
		Scope: rules.ScopeSink, Source: "builtin:kafka", Destination: "builtin:kafka",
		Topic:         `{{ index .Metadata "opencdc.collection" }}`,
		ProcessorPath: "/pipelines/0/connectors/1/processors",
		KeyShape:      smt.ShapeRaw, ValueShape: smt.ShapeRaw,
	}
	engine, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	data, err := smt.BuiltinData()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, suiteName := range []string{"regexrouter", "regexrouter-unicode"} {
		cases, err := smtoracle.LoadCases(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		suite, err := smtoracle.Load(suiteName)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range cases.Runs {
			if isFilterChain(run) {
				continue
			}
			t.Run(suiteName+"/"+run.Run, func(t *testing.T) {
				c := kcmigrate.Connector{Name: "c", Class: s3Base["connector.class"], File: "c.json"}
				cfg := chainConfig(run)
				for _, k := range sortedKeys(cfg) {
					c.Config = append(c.Config, kcmigrate.KV{Key: k, Value: kcmigrate.NewValue(cfg[k])})
				}
				ds := engine.Classify(c).Decisions
				out, err := smt.Apply(data, smt.Builtin(), env, ds)
				if err != nil {
					t.Fatal(err)
				}
				typeEntry := entryOf(ds, "transforms.r.type")
				if why, ok := refused[suiteName+"/"+run.Run]; ok {
					if typeEntry.Status != kcmigrate.StatusUnsupported || !strings.Contains(entryOf(ds, "transforms.r.regex").Reason+entryOf(ds, "transforms.r.replacement").Reason+typeEntry.Reason, why) {
						t.Fatalf("expected a refusal mentioning %q, got %+v", why, typeEntry)
					}
					return
				}
				if typeEntry.Status != kcmigrate.StatusTranslated || !out.TopicChanged {
					t.Fatalf("expected the router to translate: %+v", typeEntry)
				}
				inputs, err := cases.RecordsFor(run.Run)
				if err != nil {
					t.Fatal(err)
				}
				for _, in := range inputs {
					res, ok := suite.Find(run.Run, in.Name)
					if !ok {
						t.Fatalf("no golden for %s", in.Name)
					}
					key := suiteName + "/" + run.Run + "/" + in.Name
					if res.Result != smtoracle.ResultRecord {
						t.Errorf("%s: Kafka Connect %s; the translated router never fails a record", in.Name, res.Result)
						continue
					}
					got := evalTopic(t, out.Topic, kafkaSourceRecord(t, in))
					checked++
					switch {
					case got != res.Output.Topic && knownIllegalTopic[key]:
						t.Logf("%s: known difference on a topic name Kafka rejects (%q): Kafka Connect %q, Conduit %q", in.Name, in.Topic, res.Output.Topic, got)
					case got != res.Output.Topic:
						t.Errorf("%s (%q): Kafka Connect %q, Conduit %q", in.Name, in.Topic, res.Output.Topic, got)
					case knownIllegalTopic[key]:
						t.Errorf("%s: listed as a known difference but agrees; remove it from the list", in.Name)
					}
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("no router topic was compared")
	}
}

// legalTopic is Kafka's topic name alphabet.
var legalTopic = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,249}$`)

func init() {
	// The known differences above must stay on names Kafka rejects.
	for _, name := range []string{"kelvin-K"} {
		if legalTopic.MatchString(name) {
			panic(name + " is a legal topic name")
		}
	}
}

// isFilterChain reports whether a run is only Filters.
func isFilterChain(run smtoracle.InputRun) bool {
	for _, tr := range run.Chain {
		if tr.Type != "Filter" {
			return false
		}
	}
	return len(run.Chain) > 0
}

// chainConfig writes a run's chain as Kafka Connect config keys.
func chainConfig(run smtoracle.InputRun) map[string]string {
	cfg := map[string]string{}
	var aliases []string
	for _, tr := range run.Chain {
		aliases = append(aliases, tr.Alias)
		p := "transforms." + tr.Alias + "."
		cfg[p+"type"] = fullClass(tr.Type, "org.apache.kafka.connect.transforms.")
		for k, v := range tr.Config {
			cfg[p+k] = v
		}
		if tr.Predicate != "" {
			cfg[p+"predicate"] = tr.Predicate
		}
		if tr.Negate {
			cfg[p+"negate"] = "true"
		}
	}
	cfg["transforms"] = strings.Join(aliases, ",")
	if len(run.Predicates) > 0 {
		names := make([]string, 0, len(run.Predicates))
		for name, pr := range run.Predicates {
			names = append(names, name)
			p := "predicates." + name + "."
			cfg[p+"type"] = fullClass(pr.Type, "org.apache.kafka.connect.transforms.predicates.")
			for k, v := range pr.Config {
				cfg[p+k] = v
			}
		}
		sort.Strings(names)
		cfg["predicates"] = strings.Join(names, ",")
	}
	return cfg
}

func fullClass(typ, pkg string) string {
	if strings.Contains(typ, ".") {
		return typ
	}
	return pkg + typ
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// migrateChain runs migrate over base plus the run's chain and returns the
// result and the parsed pipeline.
func migrateChain(t *testing.T, base map[string]string, run smtoracle.InputRun) (migrate.Result, config.Pipeline) {
	t.Helper()
	cfg := map[string]string{}
	for k, v := range base {
		cfg[k] = v
	}
	for k, v := range chainConfig(run) {
		cfg[k] = v
	}
	in, err := json.Marshal(map[string]any{"name": "oracle", "config": cfg})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := migrate.Run(context.Background(), engine, migrate.Request{
		Inputs:           []kcmigrate.Input{{Name: "oracle.json", Content: in}},
		ConduitVersion:   "test",
		RequiredSettings: func(string, string) []string { return nil },
		ValidatePipeline: func(_ string, content []byte) error {
			ps, err := provyaml.NewParser(log.Nop()).Parse(context.Background(), bytes.NewReader(content))
			if err != nil {
				return err
			}
			for _, p := range ps {
				if err := config.Validate(p); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, "pipelines/") {
			ps, err := provyaml.NewParser(log.Nop()).Parse(context.Background(), bytes.NewReader(f.Content))
			if err != nil {
				t.Fatal(err)
			}
			return res, ps[0]
		}
	}
	t.Fatal("migrate wrote no pipeline")
	return res, config.Pipeline{}
}

// chainTranslated reports whether the connector's transforms entry is
// translated, after checking that every SMT key agrees with it.
func chainTranslated(t *testing.T, res migrate.Result) bool {
	t.Helper()
	rc := res.Report.Connectors[len(res.Report.Connectors)-1]
	status := kcmigrate.Status("")
	for _, e := range rc.Entries {
		if e.Key == "transforms" {
			status = e.Status
		}
	}
	if status == "" {
		t.Fatal("no transforms entry")
	}
	for _, e := range rc.Entries {
		isChain := strings.HasPrefix(e.Key, "transforms.") || strings.HasPrefix(e.Key, "predicates")
		if isChain && status == kcmigrate.StatusTranslated && e.Status != kcmigrate.StatusTranslated {
			t.Fatalf("the chain is translated but %s is %s", e.Key, e.Status)
		}
	}
	return status == kcmigrate.StatusTranslated
}

func refusalReasons(res migrate.Result) string {
	var out []string
	for _, e := range res.Report.Connectors[len(res.Report.Connectors)-1].Entries {
		if (strings.HasPrefix(e.Key, "transforms") || strings.HasPrefix(e.Key, "predicates")) && e.Status != kcmigrate.StatusTranslated {
			out = append(out, e.Key+" ("+e.Code+"): "+e.Reason)
		}
	}
	return strings.Join(out, "\n")
}

func entryOf(ds []rules.Decision, key string) kcmigrate.Entry {
	for _, d := range ds {
		if d.KV.Key == key {
			return d.Entry
		}
	}
	return kcmigrate.Entry{}
}

// chainOf returns the processors of connector idx as replay steps, and the
// destination's topic setting when it is a template.
func chainOf(t *testing.T, p config.Pipeline, idx int) ([]Step, string) {
	t.Helper()
	var steps []Step
	for _, pr := range p.Connectors[idx].Processors {
		plugin := pr.Plugin
		if !strings.Contains(plugin, ":") {
			plugin = "builtin:" + plugin
		}
		steps = append(steps, Step{Plugin: plugin, Settings: pr.Settings, Condition: pr.Condition})
	}
	topic := ""
	for _, c := range p.Connectors {
		if c.Type == config.TypeDestination && strings.Contains(c.Settings["topic"], "{{") {
			topic = c.Settings["topic"]
		}
	}
	return steps, topic
}

// evalTopic evaluates a topic template as the Kafka destination does, or
// returns "" for no template.
func evalTopic(t *testing.T, tmpl string, r opencdc.Record) string {
	t.Helper()
	if tmpl == "" {
		return ""
	}
	static, fn, err := (&destination.Config{Topic: tmpl}).ParseTopic()
	if err != nil {
		t.Fatalf("the Kafka destination rejects the topic %q: %v", tmpl, err)
	}
	if fn == nil {
		return static
	}
	got, err := fn(r)
	if err != nil {
		t.Fatalf("topic %q: %v", tmpl, err)
	}
	return got
}

// kafkaSourceRecord builds the record the Kafka source (v0.17.0, not mirror
// mode) reads for an oracle input record.
func kafkaSourceRecord(t *testing.T, in smtoracle.InputRecord) opencdc.Record {
	t.Helper()
	md := opencdc.Metadata{}
	md.SetCollection(in.Topic)
	for _, h := range in.Headers {
		var v any
		if err := json.Unmarshal(h.Value, &v); err != nil {
			t.Fatal(err)
		}
		switch x := v.(type) {
		case nil:
			md["kafka.header."+h.Key] = ""
		case string:
			md["kafka.header."+h.Key] = x
		default:
			t.Fatalf("header %s: unsupported value %v", h.Key, v)
		}
	}
	raw := func(b json.RawMessage) opencdc.Data {
		if len(b) == 0 || string(b) == "null" {
			return opencdc.RawData(nil)
		}
		return opencdc.RawData(b)
	}
	return opencdc.Record{Operation: opencdc.OperationCreate, Metadata: md, Key: raw(in.Key), Payload: opencdc.Change{After: raw(in.Value)}}
}
