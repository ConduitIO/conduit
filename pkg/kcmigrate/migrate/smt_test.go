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

package migrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt/smttest"
	"github.com/matryer/is"
)

// smtEngine is the production shared rules plus the example source class
// with one extra key that writes the Kafka destination's topic, so a chain
// starts from a known topic.
func smtEngine(t *testing.T) *rules.Engine {
	t.Helper()
	fs := fstest.MapFS{}
	for name, path := range map[string]string{
		"shared.yaml":  "../rules/data/shared.yaml",
		"example.yaml": "../rules/testdata/valid/example-source.yaml",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if name == "example.yaml" {
			b = append(b, []byte("  - key: example.topic\n    status: translated\n    emit: {kind: setting, side: destination, name: topic}\n    diff: behavior\n")...)
		}
		fs[name] = &fstest.MapFile{Data: b}
	}
	e, err := rules.Load(fs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const smtBase = sharedBase + "example.topic=orders\n"

func runSMT(t *testing.T, config string, accept bool) Result {
	t.Helper()
	res, err := Run(context.Background(), smtEngine(t), withValidator(Request{
		Inputs: []kcmigrate.Input{in("c.properties", smtBase+config)}, ConduitVersion: "v",
		Translators: smttest.Registry(), AcceptGaps: accept,
	}))
	if err != nil {
		t.Fatal(err)
	}
	is.New(t).NoErr(schema.ValidateReport(res.Files[0].Content))
	return res
}

// TestSMT_TranslatedChain: a translated chain's processors land on the
// source connector in transforms order, every SMT key points at its
// processor, the topic the chain ends with replaces the connector's, and the
// pipeline parses and validates.
func TestSMT_TranslatedChain(t *testing.T) {
	is := is.New(t)
	res := runSMT(t, "transforms=drop,split,route\n"+
		"transforms.drop.type="+smttest.Drop+"\ntransforms.drop.predicate=p\n"+
		"transforms.split.type="+smttest.Split+"$Value\ntransforms.split.a=x\n"+
		"transforms.route.type="+smttest.Route+"\n"+
		"predicates=p\npredicates.p.type="+smttest.HasMeta+"\npredicates.p.key=k\n", false)
	is.True(res.OK)
	rc := res.Report.Connectors[0]
	p := file(res, "pipelines/c.yaml")
	checkPipeline(t, p)
	for _, want := range []string{
		"processors:", "id: smt-drop", "plugin: filter", `condition: '{{ hasKey .Metadata "k" }}'`,
		"id: smt-schema-value", "id: smt-split", "plugin: field.exclude", `topic: '{{ print ("orders") "-routed" }}'`,
	} {
		is.True(strings.Contains(p, want)) // want in pipeline
	}
	is.True(strings.Index(p, "smt-drop") < strings.Index(p, "smt-split"))
	is.Equal(entryFor(rc, "transforms.split.a").Target.ConfigPath, "/pipelines/0/connectors/0/processors/2")
	is.Equal(entryFor(rc, "transforms.drop.type").Target.ConfigPath, "/pipelines/0/connectors/0/processors/0")
	is.True(entryFor(rc, "example.topic").Target == nil) // replaced by the chain's topic
	is.True(strings.Contains(entryFor(rc, "example.topic").Reason, "Replaced by the topic the transform chain ends with"))
	is.Equal(len(rc.Transforms), 3)
	is.Equal(rc.Transforms[0], kcmigrate.Transform{Alias: "drop", Class: smttest.Drop, Status: kcmigrate.StatusTranslated, Processor: "filter"})
}

// TestSMT_AcceptGapsRunsWithoutTheChain: a refused link refuses the whole
// chain; --accept-gaps lists every link and the pipeline runs with none of
// the chain's processors.
func TestSMT_AcceptGapsRunsWithoutTheChain(t *testing.T) {
	is := is.New(t)
	cfg := "transforms=drop,bad\ntransforms.drop.type=" + smttest.Drop + "\ntransforms.bad.type=com.example.Custom\n"
	res := runSMT(t, cfg, false)
	is.True(!res.OK)
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineStopped)

	res = runSMT(t, cfg, true)
	is.True(res.OK)
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineRunning)
	p := file(res, "pipelines/c.yaml")
	is.True(!strings.Contains(p, "processors:"))
	is.True(!strings.Contains(p, "-routed"))
	gaps := map[string]bool{}
	for _, g := range res.Report.AcceptedGaps {
		gaps[g.Key] = true
	}
	for _, k := range []string{"transforms", "transforms.drop.type", "transforms.bad.type"} {
		is.True(gaps[k]) // every link is an accepted gap
	}
}

// TestSMT_NoPlaceholderLeft: for every production class that produces a
// pipeline, no SMT key keeps the rule data's placeholder outcome: the SMT
// pass decides every one.
func TestSMT_NoPlaceholderLeft(t *testing.T) {
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	const placeholder = "Transforms are translated only for connector classes that produce a pipeline."
	chain := "transforms=a,b,a\ntransforms.a.type=org.apache.kafka.connect.transforms.Filter\ntransforms.a.predicate=p\n" +
		"transforms.b.x=1\ntransforms.z.type=x\npredicates=p,q\npredicates.p.type=org.apache.kafka.connect.transforms.predicates.TopicNameMatches\n" +
		"predicates.p.pattern=.*\npredicates.q.type=x\npredicates.r.type=x\n"
	for _, class := range e.Classes() {
		t.Run(class, func(t *testing.T) {
			is := is.New(t)
			res, err := Run(context.Background(), e, withValidator(Request{
				Inputs: []kcmigrate.Input{in("c.properties", "name=c\nconnector.class="+class+"\n"+chain)}, ConduitVersion: "v",
			}))
			is.NoErr(err)
			rc := res.Report.Connectors[0]
			n := 0
			for _, en := range rc.Entries {
				if strings.HasPrefix(en.Key, "transforms") || strings.HasPrefix(en.Key, "predicates") {
					n++
					if rc.Pipeline != "" {
						is.True(en.Reason != placeholder) // decided by the pass
					}
				}
			}
			is.Equal(n, 10)
			if rc.Pipeline == "" {
				is.Equal(len(rc.Transforms), 0) // no pipeline: no pass, empty index
			} else {
				is.Equal(len(rc.Transforms), 2)
			}
		})
	}
}

// TestSMT_ConverterDecidesShapeAndSchema: a sink's SMTs see what its
// effective converter (the connector's, else the worker's) produced, so the
// chain knows a side's shape only for ByteArrayConverter, and a side whose
// converter carries a schema (or is unknown) refuses reshaping SMTs
// (decision 8).
func TestSMT_ConverterDecidesShapeAndSchema(t *testing.T) {
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	const sink = "name=s\nconnector.class=io.confluent.connect.s3.S3SinkConnector\ntopics=t\ns3.bucket.name=example-b\n" +
		"transforms=w\ntransforms.w.type=" + smttest.Wrap + "$Value\n"
	const byteArray = "org.apache.kafka.connect.converters.ByteArrayConverter"
	tests := []struct {
		name, connector, worker string
		want                    string // "" for translated, else part of the reason
	}{
		{"avro", "value.converter=io.confluent.connect.avro.AvroConverter\n", "", "schema-carrying converter"},
		{"json with schemas (default)", "value.converter=org.apache.kafka.connect.json.JsonConverter\n", "", "schema-carrying converter"},
		{"json schemas.enable=true", "value.converter=org.apache.kafka.connect.json.JsonConverter\nvalue.converter.schemas.enable=true\n", "", "schema-carrying converter"},
		{"json without schemas: not bytes", "value.converter=org.apache.kafka.connect.json.JsonConverter\nvalue.converter.schemas.enable=false\n", "", "known shape"},
		{"unknown converter fails closed", "", "", "schema-carrying converter"},
		{"byte array", "value.converter=" + byteArray + "\n", "", ""},
		{"byte array from the worker", "", "value.converter=" + byteArray + "\n", ""},
		{"connector converter wins over the worker's", "value.converter=io.confluent.connect.avro.AvroConverter\n", "value.converter=" + byteArray + "\n", "schema-carrying converter"},
		{"worker schemas.enable does not reach a connector's converter", "value.converter=org.apache.kafka.connect.json.JsonConverter\n", "value.converter.schemas.enable=false\n", "schema-carrying converter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			req := Request{Inputs: []kcmigrate.Input{in("s.properties", sink+tt.connector)}, ConduitVersion: "v", Translators: smttest.Registry()}
			if tt.worker != "" {
				w := in("worker.properties", tt.worker)
				req.Worker = &w
			}
			res, err := Run(context.Background(), e, withValidator(req))
			is.NoErr(err)
			rc := res.Report.Connectors[len(res.Report.Connectors)-1]
			en := entryFor(rc, "transforms.w.type")
			if tt.want == "" {
				is.Equal(en.Status, kcmigrate.StatusTranslated)
				is.True(strings.Contains(file(res, "pipelines/s.yaml"), "id: smt-w"))
				return
			}
			is.Equal(en.Status, kcmigrate.StatusUnsupported)
			is.True(strings.Contains(en.Reason, tt.want)) // reason
		})
	}
}
