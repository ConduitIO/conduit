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
	is.True(strings.Contains(p, `topic: source.{{ index .Metadata "opencdc.collection" }}`))
	is.True(!strings.Contains(p, "mirror: ") && !strings.Contains(p, "emitMirrorMetadata")) // v0.21 never writes the mirror settings (#3038)
	var codes []string
	for _, n := range mmConnector(res).Notes {
		codes = append(codes, n.Code)
	}
	is.True(strings.Contains(strings.Join(codes, " "), "kc.output_shape_differs"))
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
	res := mmRun(t, "", "sync.topic.configs.enabled=false\nsync.topic.acls.enabled=false\n")
	for _, n := range mmConnector(res).Notes {
		is.True(!strings.Contains(n.Reason, "sync.topic"))
	}
	// Only the output-shape class note blocks now.
	blocking := 0
	for _, n := range mmConnector(res).Notes {
		if n.Blocking {
			blocking++
		}
	}
	is.Equal(blocking, 1)
}
