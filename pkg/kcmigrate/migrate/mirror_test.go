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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

const mmBase = "name=mm\nconnector.class=org.apache.kafka.connect.mirror.MirrorSourceConnector\n" +
	"key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n" +
	"source.cluster.bootstrap.servers=src:9092\ntarget.cluster.bootstrap.servers=tgt:9092\n"

func mmRun(t *testing.T, worker, extra string) Result {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extra, "topics=") {
		extra += "topics=orders\n"
	}
	req := Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("mm.properties", mmBase+extra)}}
	if worker != "" {
		w := in("w.properties", worker)
		req.Worker = &w
	}
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mmConnector(res Result) kcmigrate.ReportConnector {
	return res.Report.Connectors[len(res.Report.Connectors)-1]
}

func TestMirror_Defaults(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "")
	p := file(res, "pipelines/mm.yaml")
	is.True(strings.Contains(p, "servers: src:9092"))
	is.True(strings.Contains(p, "servers: tgt:9092"))
	is.True(!strings.Contains(p, "topic: "))                                                // source.cluster.alias is required and absent
	is.True(!strings.Contains(p, "mirror: ") && !strings.Contains(p, "emitMirrorMetadata")) // v0.21 never writes the mirror settings (#3038)
	var codes []string
	for _, n := range mmConnector(res).Notes {
		codes = append(codes, n.Code)
	}
	is.True(strings.Contains(strings.Join(codes, " "), "kc.output_shape_differs"))
	is.True(strings.Contains(strings.Join(codes, " "), "kc.key_required"))
	is.True(!res.OK)
	is.Equal(mmConnector(res).PipelineStatus, kcmigrate.PipelineStopped)
}

// Kafka Connect's producer writes to the worker's cluster: the worker's
// bootstrap.servers and producer.override.bootstrap.servers win over
// target.cluster.bootstrap.servers.
func TestMirror_DestinationServersLayers(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "bootstrap.servers=worker:9092\n", "")
	p := file(res, "pipelines/mm.yaml")
	is.True(strings.Contains(p, "servers: worker:9092"))
	is.True(!strings.Contains(p, "tgt:9092"))

	res = mmRun(t, "bootstrap.servers=worker:9092\n", "producer.override.bootstrap.servers=override:9092\n")
	is.True(strings.Contains(file(res, "pipelines/mm.yaml"), "servers: override:9092"))
}

// MirrorMaker's consumer merges source.cluster.* < consumer.* < source.consumer.*.
func TestMirror_SourceConsumerLayers(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "source.cluster.sasl.mechanism=PLAIN\nconsumer.sasl.mechanism=SCRAM-SHA-256\nsource.consumer.sasl.mechanism=SCRAM-SHA-512\n")
	p := file(res, "pipelines/mm.yaml")
	is.True(strings.Contains(p, "saslMechanism: SCRAM-SHA-512"))
	e := entryFor(mmConnector(res), "source.cluster.sasl.mechanism")
	is.True(e.Target == nil)
	is.True(strings.Contains(e.Reason, "Overridden by"))
}

func TestMirror_TopicsAndPolicy(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "topics=orders.v1, payments\n")
	e := entryFor(mmConnector(res), "topics")
	is.Equal(e.Status, kcmigrate.StatusApproximated) // '.' is a regex wildcard in MirrorMaker
	is.True(strings.Contains(file(res, "pipelines/mm.yaml"), "topics: orders.v1,payments"))

	res = mmRun(t, "", "replication.policy.class=org.apache.kafka.connect.mirror.IdentityReplicationPolicy\nsource.cluster.alias=us\n")
	is.True(strings.Contains(file(res, "pipelines/mm.yaml"), `topic: '{{ index .Metadata "opencdc.collection" }}'`))

	res = mmRun(t, "", "source.cluster.alias=us\nreplication.policy.separator=_\n")
	is.True(strings.Contains(file(res, "pipelines/mm.yaml"), `topic: us_{{ index .Metadata "opencdc.collection" }}`))
}

func TestMirror_DisabledSyncsAreClean(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "source.cluster.alias=src\nsync.topic.configs.enabled=false\nsync.topic.acls.enabled=false\n")
	for _, n := range mmConnector(res).Notes {
		is.True(!strings.Contains(n.Reason, "sync.topic"))
	}
	// Only the class notes block now: output shape and stored offsets.
	var blocking []string
	for _, n := range mmConnector(res).Notes {
		if n.Blocking {
			blocking = append(blocking, n.Code)
		}
	}
	is.Equal(blocking, []string{"kc.output_shape_differs", "kc.offsets_not_migrated"})
}

// The worker configures Kafka Connect's producer only: none of its keys
// reach MirrorMaker's own consumer of the source cluster, and a missing
// source cluster is a blocking required setting, not the worker's cluster.
func TestMirror_WorkerDoesNotReachSource(t *testing.T) {
	is := is.New(t)
	worker := "bootstrap.servers=worker:9092\nconsumer.bootstrap.servers=wc:9092\nconsumer.security.protocol=SASL_SSL\nconsumer.sasl.mechanism=PLAIN\n" +
		`consumer.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="w" password="WORKERCANARY";` + "\n"
	res := mmRun(t, worker, "source.cluster.security.protocol=SASL_SSL\nsource.cluster.sasl.mechanism=SCRAM-SHA-512\n")
	src := settings(t, res, "pipelines/mm.yaml", "source")
	is.Equal(src["servers"], "src:9092")
	is.Equal(src["saslMechanism"], "SCRAM-SHA-512")
	_, ok := src["saslUsername"]
	is.True(!ok)
	is.True(!strings.Contains(file(res, SecretsTemplateFile), "CONSUMER_SASL_JAAS"))

	// No source.cluster.bootstrap.servers: required, never the worker's.
	e, err := rules.Builtin()
	is.NoErr(err)
	w := in("w.properties", worker)
	cfg := strings.Replace(mmBase, "source.cluster.bootstrap.servers=src:9092\n", "", 1) + "topics=orders\n"
	res, err = Run(context.Background(), e, withValidator(Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Worker: &w, Inputs: []kcmigrate.Input{in("mm.properties", cfg)}}))
	is.NoErr(err)
	is.Equal(settings(t, res, "pipelines/mm.yaml", "source")["servers"], "${KC_MM_SOURCE_SERVERS}")
	var required bool
	for _, n := range mmConnector(res).Notes {
		required = required || (n.Rule == "required/source/servers" && n.Blocking)
	}
	is.True(required)
}

// MirrorMaker resumes from the offsets Kafka Connect stored, else from the
// beginning. The offsets are not carried over: a blocking note says so, the
// pipeline reads from the beginning like MirrorMaker without offsets, and
// --accept-gaps names the note in acceptedGaps.
func TestMirror_StartPosition(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "")
	is.Equal(settings(t, res, "pipelines/mm.yaml", "source")["readFromBeginning"], "true")
	var note bool
	for _, n := range mmConnector(res).Notes {
		note = note || (n.Code == "kc.offsets_not_migrated" && n.Blocking)
	}
	is.True(note)

	res = mmRun(t, "", "consumer.auto.offset.reset=latest\n")
	is.Equal(settings(t, res, "pipelines/mm.yaml", "source")["readFromBeginning"], "false")
	res = mmRun(t, "", "consumer.auto.offset.reset=latest\nsource.consumer.auto.offset.reset=earliest\n")
	is.Equal(settings(t, res, "pipelines/mm.yaml", "source")["readFromBeginning"], "true") // source.consumer. wins
	res = mmRun(t, "", "consumer.auto.offset.reset=none\n")
	is.True(entryFor(mmConnector(res), "consumer.auto.offset.reset").Blocking)

	e, err := rules.Builtin()
	is.NoErr(err)
	res, err = Run(context.Background(), e, withValidator(Request{ConduitVersion: "v", AcceptGaps: true, RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("mm.properties", mmBase+"topics=orders\n")}}))
	is.NoErr(err)
	var accepted bool
	for _, g := range res.Report.AcceptedGaps {
		accepted = accepted || g.Code == "kc.offsets_not_migrated"
	}
	is.True(accepted)
}

func TestMirror_IsolationLevel(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "consumer.isolation.level=read_uncommitted\n")
	e := entryFor(mmConnector(res), "consumer.isolation.level")
	is.Equal(e.Status, kcmigrate.StatusTranslated)
	for _, k := range []string{"consumer.isolation.level", "source.consumer.isolation.level"} {
		res = mmRun(t, "", k+"=read_committed\n")
		is.True(entryFor(mmConnector(res), k).Blocking)
	}
}

// enabled=false: Kafka Connect runs no tasks, so the pipeline is written
// stopped, also under --accept-gaps, and the entry itself does not block.
func TestMirror_Disabled(t *testing.T) {
	is := is.New(t)
	e, err := rules.Builtin()
	is.NoErr(err)
	res, err := Run(context.Background(), e, withValidator(Request{ConduitVersion: "v", AcceptGaps: true, RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("mm.properties", mmBase+"topics=orders\nenabled=false\n")}}))
	is.NoErr(err)
	en := entryFor(mmConnector(res), "enabled")
	is.Equal(en.Status, kcmigrate.StatusTranslated)
	is.True(!en.Blocking)
	is.Equal(mmConnector(res).PipelineStatus, kcmigrate.PipelineStopped)
	is.True(strings.Contains(file(res, "pipelines/mm.yaml"), "status: stopped"))
}

func TestMirror_NITs(t *testing.T) {
	is := is.New(t)
	// MirrorMaker's default topics.exclude drops these even when listed.
	for _, topics := range []string{"orders,__consumer_offsets", "a.replica", "mm2-offsets.us.internal, orders"} {
		res := mmRun(t, "", "topics="+topics+"\n")
		is.True(entryFor(mmConnector(res), "topics").Blocking)
	}
	res := mmRun(t, "", "topics=orders,my__topic\n")
	is.True(!entryFor(mmConnector(res), "topics").Blocking)

	// consumer.override.ssl.* reaches only the offsets consumer.
	res = mmRun(t, "", "consumer.override.ssl.truststore.location=/t.jks\n")
	is.True(!entryFor(mmConnector(res), "consumer.override.ssl.truststore.location").Blocking)

	// A custom config property filter matters only while configs sync.
	res = mmRun(t, "", "sync.topic.configs.enabled=false\nconfig.property.filter.class=com.example.Filter\n")
	is.True(!entryFor(mmConnector(res), "config.property.filter.class").Blocking)
}

// The offsets note says where the pipeline actually starts.
func TestMirror_OffsetsNoteFollowsStartPosition(t *testing.T) {
	is := is.New(t)
	reason := func(res Result) string {
		for _, n := range mmConnector(res).Notes {
			if n.Code == "kc.offsets_not_migrated" {
				is.True(n.Blocking)
				return n.Reason
			}
		}
		t.Fatal("no kc.offsets_not_migrated note")
		return ""
	}
	is.True(strings.Contains(reason(mmRun(t, "", "")), "readFromBeginning: true"))
	is.True(strings.Contains(reason(mmRun(t, "", "consumer.auto.offset.reset=latest\n")), "readFromBeginning: false"))
	res := mmRun(t, "", "consumer.auto.offset.reset=none\n")
	_, ok := settings(t, res, "pipelines/mm.yaml", "source")["readFromBeginning"]
	is.True(!ok) // the blocked key owns it
	is.True(strings.Contains(reason(res), "not written"))
}

// A fallback cluster that a higher layer overrides in part says it is not
// used as a whole.
func TestMirror_PartialFallbackIsReported(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "producer.sasl.jaas.config=org.apache.kafka.common.security.kerberos.Nope required;\n",
		"target.cluster.security.protocol=SASL_SSL\ntarget.cluster.sasl.mechanism=PLAIN\n"+
			`target.cluster.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="t" password="p";`+"\n")
	is.Equal(settings(t, res, "pipelines/mm.yaml", "destination")["servers"], "tgt:9092")
	e := entryFor(mmConnector(res), "target.cluster.bootstrap.servers")
	is.True(strings.Contains(e.Reason, "not used as a whole"))
	is.True(strings.Contains(e.Reason, "target.cluster.sasl.jaas.config"))
}

// source.cluster.alias has no default in Apache Kafka 4.2
// (MirrorConnectorConfig.java:270-273): a config without it is one Kafka
// Connect rejects. The run blocks on it and writes no topic prefix it would
// have to guess.
func TestMirror_AliasRequired(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "")
	var note *kcmigrate.Note
	for i, n := range mmConnector(res).Notes {
		if n.Code == "kc.key_required" {
			note = &mmConnector(res).Notes[i]
		}
	}
	is.True(note != nil)
	is.True(note.Blocking)
	is.True(strings.Contains(note.Reason, "source.cluster.alias"))
	_, ok := settings(t, res, "pipelines/mm.yaml", "destination")["topic"]
	is.True(!ok) // no guessed "source." prefix

	res = mmRun(t, "", "source.cluster.alias=us\n")
	for _, n := range mmConnector(res).Notes {
		is.True(n.Code != "kc.key_required")
	}
	is.Equal(settings(t, res, "pipelines/mm.yaml", "destination")["topic"], `us.{{ index .Metadata "opencdc.collection" }}`)
}

func auxRun(t *testing.T, accept bool, config string) Result {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), e, withValidator(Request{ConduitVersion: "v", AcceptGaps: accept, RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("c.properties", config)}}))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// MirrorHeartbeatConnector copies no data: no pipeline, every key
// unsupported and non-blocking, so the run passes.
func TestMirror_HeartbeatNoPipeline(t *testing.T) {
	is := is.New(t)
	res := auxRun(t, false, "name=hb\nconnector.class=org.apache.kafka.connect.mirror.MirrorHeartbeatConnector\nsource.cluster.alias=a\nemit.heartbeats.interval.seconds=5\n")
	rc := mmConnector(res)
	is.Equal(len(rc.Entries), 4)
	for _, e := range rc.Entries {
		is.Equal(e.Status, kcmigrate.StatusUnsupported)
		is.Equal(e.Code, kcmigrate.CodeConnectorClassUnsupported.Reason())
		is.True(!e.Blocking)
	}
	is.Equal(rc.Pipeline, "")
	is.Equal(rc.PipelineStatus, kcmigrate.PipelineStatus(""))
	is.True(res.OK)
	// Retiring it stops the heartbeats; the note says so, without blocking.
	var stops bool
	for _, n := range rc.Notes {
		stops = stops || (n.Code == kcmigrate.CodeOutputShapeDiffers.Reason() && !n.Blocking && strings.Contains(n.Reason, "Heartbeats stop"))
	}
	is.True(stops)
	is.True(strings.Contains(file(res, "REPORT.md"), "Heartbeats stop"))
	for _, f := range res.Files {
		is.True(!strings.HasPrefix(f.Path, "pipelines/"))
	}
}

// MirrorCheckpointConnector translates consumer offsets, which has no
// equivalent: no pipeline and every key blocks. A literal secret is still
// noted, since the input holds it.
func TestMirror_CheckpointNoPipeline(t *testing.T) {
	is := is.New(t)
	cfg := "name=cp\nconnector.class=org.apache.kafka.connect.mirror.MirrorCheckpointConnector\ngroups=.*\n" +
		`target.cluster.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="u" password="CPCANARY";` + "\n"
	res := auxRun(t, false, cfg)
	rc := mmConnector(res)
	for _, e := range rc.Entries {
		is.Equal(e.Code, kcmigrate.CodeConnectorClassUnsupported.Reason())
		is.True(e.Blocking)
	}
	is.True(entryFor(rc, "target.cluster.sasl.jaas.config").Secret)
	var plaintext bool
	for _, n := range rc.Notes {
		plaintext = plaintext || n.Code == kcmigrate.CodeSecretPlaintext.Reason()
	}
	is.True(plaintext)
	is.True(!res.OK)
	is.Equal(rc.Pipeline, "")
	for _, f := range res.Files {
		is.True(!strings.Contains(string(f.Content), "CPCANARY"))
	}
	is.True(auxRun(t, true, cfg).OK) // accepted gaps
}
