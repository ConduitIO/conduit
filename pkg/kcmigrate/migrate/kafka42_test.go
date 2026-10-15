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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// kafka42Config is a MirrorSourceConnector config written for Kafka 4.2:
// both clusters with SASL, a worker, consumer and producer layers, topic
// naming, the sync switches, tuning keys and an absent alias's sibling keys.
const kafka42Config = "name=mm42\nconnector.class=org.apache.kafka.connect.mirror.MirrorSourceConnector\n" +
	"key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n" +
	"source.cluster.alias=east\ntarget.cluster.alias=west\nreplication.policy.separator=_\n" +
	"source.cluster.bootstrap.servers=east.internal.example:9096\nsource.cluster.security.protocol=SASL_SSL\nsource.cluster.sasl.mechanism=SCRAM-SHA-512\n" +
	`source.cluster.sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required username="u" password="${file:/etc/kc/secrets:east}";` + "\n" +
	"target.cluster.bootstrap.servers=west.internal.example:9096\ntarget.cluster.security.protocol=SASL_SSL\n" +
	"consumer.auto.offset.reset=latest\nconsumer.isolation.level=read_uncommitted\nsource.consumer.max.poll.records=500\n" +
	"producer.override.compression.type=zstd\nproducer.override.acks=all\n" +
	"topics=orders,payments\nrefresh.topics.enabled=false\nsync.topic.configs.enabled=false\nsync.topic.acls.enabled=false\n" +
	"emit.offset-syncs.enabled=false\nheartbeats.replication.enabled=false\nconfig.properties.exclude=min\\.insync\\.replicas\n" +
	"replication.factor=3\noffset.lag.max=200\ntasks.max=4\n"

// kafka42Golden was captured with the rules pinned to Kafka 4.2. It is not
// regenerated: moving the pin to a newer Kafka must leave a 4.2-era config's
// pipeline, secrets and entries exactly as they were.
const kafka42Golden = "testdata/kafka42/mm42.golden"

var reportHash = regexp.MustCompile(`report sha256:[0-9a-f]{64}`)

func kafka42Capture(t *testing.T) string {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	w := in("worker.properties", "bootstrap.servers=west.internal.example:9096\nproducer.security.protocol=SASL_SSL\n")
	res, err := Run(context.Background(), e, withValidator(Request{
		ConduitVersion: "v", RequiredSettings: BuiltinRequired, Worker: &w,
		Inputs: []kcmigrate.Input{in("mm42.properties", kafka42Config)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	// The description names the report's hash, and the report names the
	// rules' upstream version, which is what moves; everything that runs is
	// compared exactly.
	pipeline := reportHash.ReplaceAllString(file(res, "pipelines/mm42.yaml"), "report sha256:<hash>")
	b.WriteString("## pipelines/mm42.yaml\n" + pipeline)
	b.WriteString("## " + SecretsTemplateFile + "\n" + file(res, SecretsTemplateFile))
	for _, rc := range res.Report.Connectors {
		fmt.Fprintf(&b, "## %s (%s) pipeline=%s status=%s\n", rc.Name, rc.Role, rc.Pipeline, rc.PipelineStatus)
		for _, e := range rc.Entries {
			target := ""
			if e.Target != nil {
				target = e.Target.ConfigPath + " " + e.Target.Env
			}
			fmt.Fprintf(&b, "entry %s %s %s blocking=%v secret=%v target=%s\n", e.Key, e.Status, e.Code, e.Blocking, e.Secret, target)
		}
		for _, n := range rc.Notes {
			fmt.Fprintf(&b, "note %s blocking=%v rule=%s\n", n.Code, n.Blocking, n.Rule)
		}
	}
	return b.String()
}

func TestMirror_Kafka42ConfigMigratesUnchanged(t *testing.T) {
	got := kafka42Capture(t)
	if os.Getenv("KCMIGRATE_CAPTURE_KAFKA42") == "1" {
		if err := os.MkdirAll(filepath.Dir(kafka42Golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kafka42Golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(kafka42Golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("a Kafka 4.2-era MirrorMaker config no longer migrates as it did under the 4.2 pin.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// metric.names.formats (new in Kafka 4.3) names MirrorMaker's own metrics:
// translated with no effect on the pipeline, whatever its value.
func TestMirror_MetricNamesFormats(t *testing.T) {
	for _, v := range []string{"legacy", "new", "legacy,new"} {
		res := mmRun(t, "", "source.cluster.alias=a\nmetric.names.formats="+v+"\n")
		e := entryFor(mmConnector(res), "metric.names.formats")
		if e.Status != kcmigrate.StatusTranslated || e.Blocking || e.Target != nil {
			t.Errorf("metric.names.formats=%s: %+v, want translated with no target", v, e)
		}
	}
}
