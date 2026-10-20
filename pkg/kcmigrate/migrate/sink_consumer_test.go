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

// These tests pin how a migrated sink's Kafka source is positioned. Kafka
// Connect builds a sink's consumer with group.id=connect-<name> and
// auto.offset.reset=earliest, then applies the worker's consumer.* keys, then
// the connector's consumer.override.* keys (Worker.baseConsumerConfigs,
// Apache Kafka 4.2.0, Worker.java:911-942). The Kafka source's own defaults
// (a random group, the end of each partition) would skip every record
// written before the pipeline starts (invariant 3), issue #3089.

const s3SinkBase = "name=orders-sink\nconnector.class=io.confluent.connect.s3.S3SinkConnector\n" +
	"key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n" +
	"topics=orders\ns3.bucket.name=b\ns3.region=us-east-1\nstorage.class=io.confluent.connect.s3.storage.S3Storage\n" +
	"format.class=io.confluent.connect.s3.format.parquet.ParquetFormat\nflush.size=10\n"

func sinkRun(t *testing.T, worker, extra string) (Result, string) {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	req := Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("sink.properties", s3SinkBase+extra)}}
	if worker != "" {
		w := in("w.properties", worker)
		req.Worker = &w
	}
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res, file(res, "pipelines/orders-sink.yaml")
}

func noteByRule(rc kcmigrate.ReportConnector, rule string) (kcmigrate.Note, bool) {
	for _, n := range rc.Notes {
		if strings.HasSuffix(n.Rule, rule) {
			return n, true
		}
	}
	return kcmigrate.Note{}, false
}

// The regression for #3089: with nothing set, the sink joins Kafka Connect's
// group and starts at the beginning, and the report says both.
func TestSinkConsumer_Defaults(t *testing.T) {
	is := is.New(t)
	res, p := sinkRun(t, "", "")
	is.True(strings.Contains(p, "groupID: connect-orders-sink"))
	is.True(strings.Contains(p, `readFromBeginning: "true"`))

	rc := res.Report.Connectors[0]
	g, ok := noteByRule(rc, "sink-consumer-defaults/groupID")
	is.True(ok)
	is.Equal(g.Code, "kc.default_differs")
	is.True(!g.Blocking)
	is.True(strings.Contains(g.Reason, "connect-orders-sink"))
	r, ok := noteByRule(rc, "sink-consumer-defaults/readFromBeginning")
	is.True(ok)
	is.Equal(r.Code, "kc.default_differs")
	is.True(!r.Blocking)
	is.True(strings.Contains(r.Reason, "earliest"))

	c, ok := noteByRule(rc, "sink-consumer-defaults/cutover")
	is.True(ok)
	is.Equal(c.Code, "kc.consumer_group_shared")
	is.True(!c.Blocking)
	is.True(strings.Contains(c.Reason, "connect-orders-sink"))
	is.True(strings.Contains(c.Fix, "Stop the Kafka Connect connector"))
	is.True(strings.Contains(c.Fix, "--group connect-orders-sink"))
}

// Kafka Connect's group is connect-<name> with the name verbatim.
func TestSinkConsumer_GroupKeepsNameVerbatim(t *testing.T) {
	is := is.New(t)
	e, err := rules.Builtin()
	is.NoErr(err)
	cfg := strings.Replace(s3SinkBase, "name=orders-sink", "name=My.Sink_1", 1)
	res, err := Run(context.Background(), e, withValidator(Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("s.properties", cfg)}}))
	is.NoErr(err)
	is.True(strings.Contains(file(res, "pipelines/My.Sink_1.yaml"), "groupID: connect-My.Sink_1"))
}

// consumer.override.* is the top layer: its group and reset win, nothing is
// defaulted, and the entries are the report of them.
func TestSinkConsumer_ConnectorOverrides(t *testing.T) {
	is := is.New(t)
	res, p := sinkRun(t, "", "consumer.override.group.id=shared-group\nconsumer.override.auto.offset.reset=latest\n")
	is.True(strings.Contains(p, "groupID: shared-group"))
	is.True(strings.Contains(p, `readFromBeginning: "false"`))
	is.True(!strings.Contains(p, "connect-orders-sink"))

	rc := res.Report.Connectors[0]
	for _, k := range []string{"consumer.override.group.id", "consumer.override.auto.offset.reset"} {
		is.Equal(entryFor(rc, k).Status, kcmigrate.StatusTranslated)
		is.True(entryFor(rc, k).Target != nil)
	}
	_, ok := noteByRule(rc, "sink-consumer-defaults/groupID")
	is.True(!ok)
	_, ok = noteByRule(rc, "sink-consumer-defaults/readFromBeginning")
	is.True(!ok)
	c, ok := noteByRule(rc, "sink-consumer-defaults/cutover")
	is.True(ok)
	is.True(strings.Contains(c.Reason, "shared-group")) // the cutover note follows the group in effect
}

// The worker's consumer.* keys replace Kafka Connect's defaults for every
// sink, and the connector's override replaces the worker's.
func TestSinkConsumer_WorkerLayer(t *testing.T) {
	is := is.New(t)
	worker := "bootstrap.servers=k:9092\nkey.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n" +
		"consumer.group.id=worker-group\nconsumer.auto.offset.reset=latest\n"
	res, p := sinkRun(t, worker, "")
	is.True(strings.Contains(p, "groupID: worker-group"))
	is.True(strings.Contains(p, `readFromBeginning: "false"`))
	w := res.Report.Connectors[0]
	for _, k := range []string{"consumer.group.id", "consumer.auto.offset.reset"} {
		is.Equal(entryFor(w, k).Status, kcmigrate.StatusTranslated)
		is.True(!entryFor(w, k).Blocking)
	}
	_, ok := noteByRule(res.Report.Connectors[1], "sink-consumer-defaults/groupID")
	is.True(!ok)

	_, p = sinkRun(t, worker, "consumer.override.group.id=own\nconsumer.override.auto.offset.reset=earliest\n")
	is.True(strings.Contains(p, "groupID: own"))
	is.True(strings.Contains(p, `readFromBeginning: "true"`))
	is.True(!strings.Contains(p, "worker-group"))
}

// A value migrate cannot carry (auto.offset.reset=none fails on a missing
// offset in Kafka Connect) blocks, and the default must not fill in under
// it: the pipeline is stopped and writes no start position of its own.
func TestSinkConsumer_BlockedResetIsNotDefaulted(t *testing.T) {
	for name, tc := range map[string]struct{ worker, extra, key string }{
		"connector": {"", "consumer.override.auto.offset.reset=none\n", "consumer.override.auto.offset.reset"},
		"worker": {
			"bootstrap.servers=k:9092\nkey.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nconsumer.auto.offset.reset=none\n",
			"", "consumer.auto.offset.reset",
		},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			res, p := sinkRun(t, tc.worker, tc.extra)
			is.True(!strings.Contains(p, "readFromBeginning"))
			is.True(strings.Contains(p, "groupID: connect-orders-sink")) // the group is still Kafka Connect's
			rc := res.Report.Connectors[len(res.Report.Connectors)-1]
			_, ok := noteByRule(rc, "sink-consumer-defaults/readFromBeginning")
			is.True(!ok)
			is.Equal(rc.PipelineStatus, kcmigrate.PipelineStopped)
			is.True(!res.OK)
			var blocking bool
			for _, c := range res.Report.Connectors {
				if e := entryFor(c, tc.key); e.Blocking {
					blocking = true
				}
			}
			is.True(blocking)
		})
	}
}

// A source connector has no consumer to position: nothing is written to its
// pipeline.
func TestSinkConsumer_NotWrittenForSources(t *testing.T) {
	is := is.New(t)
	res := mmRun(t, "", "")
	p := file(res, "pipelines/mm.yaml")
	is.True(!strings.Contains(p, "groupID: connect-"))
	_, ok := noteByRule(mmConnector(res), "sink-consumer-defaults/cutover")
	is.True(!ok)
}

const wkBase = "bootstrap.servers=k:9092\nkey.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n"

// A worker consumer.auto.offset.reset Conduit cannot carry (none,
// by_duration:...) is read only by sink connectors' consumers, so it blocks a
// sink and nothing else. A MirrorMaker or source run is not stopped by it.
func TestSinkConsumer_WorkerResetBlocksOnlySinks(t *testing.T) {
	for _, v := range []string{"none", "by_duration:PT1H"} {
		t.Run(v, func(t *testing.T) {
			is := is.New(t)
			worker := wkBase + "consumer.auto.offset.reset=" + v + "\n"

			// Sink run: blocking worker entry, stopped pipeline, no start position written.
			res, p := sinkRun(t, worker, "")
			is.True(entryFor(res.Report.Connectors[0], "consumer.auto.offset.reset").Blocking)
			is.Equal(res.Report.Connectors[1].PipelineStatus, kcmigrate.PipelineStopped)
			is.True(!res.OK)
			is.True(!strings.Contains(p, "readFromBeginning"))

			// MirrorMaker-only run: the entry is reported, does not block, and
			// changes neither the blocking count nor the verdict.
			base := mmRun(t, wkBase, "source.cluster.alias=src\n")
			mm := mmRun(t, worker, "source.cluster.alias=src\n")
			e := entryFor(mm.Report.Connectors[0], "consumer.auto.offset.reset")
			is.Equal(e.Status, kcmigrate.StatusUnsupported)
			is.True(!e.Blocking)
			is.True(e.Fix != "")
			is.Equal(mm.Report.Summary.Blocking, base.Report.Summary.Blocking)
			is.Equal(mmConnector(mm).PipelineStatus, mmConnector(base).PipelineStatus)
			is.Equal(mm.OK, base.OK)
		})
	}
}

// An unknown reset value is invalid in Kafka itself and still blocks every
// pipeline of the run.
func TestSinkConsumer_WorkerResetInvalidBlocksAll(t *testing.T) {
	is := is.New(t)
	mm := mmRun(t, wkBase+"consumer.auto.offset.reset=sometimes\n", "source.cluster.alias=src\n")
	is.True(entryFor(mm.Report.Connectors[0], "consumer.auto.offset.reset").Blocking)
	is.Equal(mmConnector(mm).PipelineStatus, kcmigrate.PipelineStopped)
}

// In a run with a sink and a MirrorMaker connector, only the sink is held by
// the worker key. With --accept-gaps the sink runs.
func TestSinkConsumer_WorkerResetMixedRun(t *testing.T) {
	is := is.New(t)
	e, err := rules.Builtin()
	is.NoErr(err)
	w := in("w.properties", wkBase+"consumer.auto.offset.reset=none\n")
	req := Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Worker: &w, Inputs: []kcmigrate.Input{
		in("sink.properties", s3SinkBase),
		in("mm.properties", mmBase+"topics=orders\nsource.cluster.alias=src\n"),
	}}
	res, err := Run(context.Background(), e, withValidator(req))
	is.NoErr(err)
	byName := map[string]kcmigrate.ReportConnector{}
	for _, c := range res.Report.Connectors {
		byName[c.Name] = c
	}
	is.Equal(byName["orders-sink"].PipelineStatus, kcmigrate.PipelineStopped)
	// The MirrorMaker pipeline's status comes from its own blocking notes
	// only (the offsets note), exactly as without the worker key.
	req.Worker = nil
	base, err := Run(context.Background(), e, withValidator(req))
	is.NoErr(err)
	for _, c := range base.Report.Connectors {
		if c.Name == "mm" {
			is.Equal(byName["mm"].PipelineStatus, c.PipelineStatus)
		}
	}
	req.Worker = &w
	req.AcceptGaps = true
	acc, err := Run(context.Background(), e, withValidator(req))
	is.NoErr(err)
	for _, c := range acc.Report.Connectors {
		if c.Name == "orders-sink" {
			is.Equal(c.PipelineStatus, kcmigrate.PipelineRunning)
		}
	}
}
