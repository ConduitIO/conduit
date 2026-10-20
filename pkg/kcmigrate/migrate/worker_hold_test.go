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
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

// Every built-in class carries a blocking class note, which stops its
// pipeline whatever a worker key does, so the built-in classes cannot show
// which pipelines a worker key holds. These classes have no notes: only the
// worker key can stop their pipelines.

const (
	holdSink = `rulesVersion: 1
scope: sink
class: com.example.HoldSink
upstream: {name: example, version: "1.0"}
emits:
  - {plugin: builtin:kafka, role: source}
  - {plugin: builtin:log, role: destination}
keys: []
`
	holdSource = `rulesVersion: 1
scope: source
class: com.example.HoldSource
upstream: {name: example, version: "1.0"}
emits:
  - {plugin: builtin:log, role: source}
  - {plugin: builtin:kafka, role: destination}
keys: []
`
	holdConv = "key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n"
)

func holdRun(t *testing.T, workerReset, sinkExtra string, accept bool) map[string]kcmigrate.PipelineStatus {
	t.Helper()
	fs := fstest.MapFS{
		"sink.yaml":   {Data: []byte(holdSink)},
		"source.yaml": {Data: []byte(holdSource)},
	}
	for name, path := range map[string]string{"shared.yaml": "../rules/data/shared.yaml", "worker.yaml": "../rules/data/worker.yaml"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fs[name] = &fstest.MapFile{Data: b}
	}
	e, err := rules.Load(fs)
	if err != nil {
		t.Fatal(err)
	}
	w := in("w.properties", "bootstrap.servers=k:9092\n"+holdConv+"consumer.auto.offset.reset="+workerReset+"\n")
	req := Request{ConduitVersion: "v", Worker: &w, AcceptGaps: accept, Inputs: []kcmigrate.Input{
		in("sink.properties", "name=snk\nconnector.class=com.example.HoldSink\ntopics=t\n"+holdConv+sinkExtra),
		in("source.properties", "name=src\nconnector.class=com.example.HoldSource\n"+holdConv),
	}}
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]kcmigrate.PipelineStatus{}
	for _, c := range res.Report.Connectors[1:] {
		out[c.Name] = c.PipelineStatus
		for _, n := range c.Notes {
			if n.Blocking {
				t.Fatalf("%s has a blocking note %s; the test needs the worker key to be the only blocker", c.Name, n.Code)
			}
		}
	}
	return out
}

func TestWorkerHold_ResetNoneStopsSinksOnly(t *testing.T) {
	is := is.New(t)
	got := holdRun(t, "none", "", false)
	is.Equal(got["snk"], kcmigrate.PipelineStopped)
	is.Equal(got["src"], kcmigrate.PipelineRunning)
}

func TestWorkerHold_InvalidResetStopsEverything(t *testing.T) {
	is := is.New(t)
	got := holdRun(t, "sometimes", "", false)
	is.Equal(got["snk"], kcmigrate.PipelineStopped)
	is.Equal(got["src"], kcmigrate.PipelineStopped)
}

func TestWorkerHold_AcceptGapsRunsBoth(t *testing.T) {
	is := is.New(t)
	for _, v := range []string{"none", "sometimes"} {
		got := holdRun(t, v, "", true)
		is.Equal(got["snk"], kcmigrate.PipelineRunning)
		is.Equal(got["src"], kcmigrate.PipelineRunning)
	}
}

// The connector's own consumer.override.auto.offset.reset outranks the
// worker's value, which then never reaches the sink's consumer: it is not held.
func TestWorkerHold_SinkOverrideOutranksWorkerReset(t *testing.T) {
	is := is.New(t)
	got := holdRun(t, "none", "consumer.override.auto.offset.reset=latest\n", false)
	is.Equal(got["snk"], kcmigrate.PipelineRunning)
	is.Equal(got["src"], kcmigrate.PipelineRunning)
}
