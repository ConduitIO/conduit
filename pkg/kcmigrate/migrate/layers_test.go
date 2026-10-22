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
	"github.com/conduitio/yaml/v3"
	"github.com/matryer/is"
)

// testMirrorClass is a Kafka Connect source whose pipeline has a Kafka
// connector on both sides, as MirrorMaker's does. Kafka Connect's own
// client for it is the producer, the pipeline's destination; the pipeline's
// source is the connector's own consumer, which no worker key reaches.
const testMirrorClass = `rulesVersion: 1
scope: source
class: com.example.Mirror
upstream: {name: example, version: "1.0"}
emits:
  - {plugin: builtin:kafka, role: source}
  - {plugin: builtin:kafka, role: destination}
keys:
  - key: producer.sasl.mechanism
    status: translated
    emit: {kind: setting, side: destination, name: saslMechanism, layer: client}
    diff: identity
  - key: x.sasl.mechanism
    diff: identity
    values:
      - match: [PLAIN, SCRAM-SHA-512]
        status: translated
        emit: {kind: setting, side: destination, name: saslMechanism, layer: cluster-client}
      - otherwise: true
        status: unsupported
        blocking: true
        code: kc.key_unsupported
        reason: Not supported.
        fix: Change it.
  - key: source.cluster.bootstrap.servers
    status: translated
    emit: {kind: setting, side: source, name: servers}
    diff: identity
  - key: topics
    status: translated
    emit: {kind: list-setting, side: source, name: topics}
    diff: identity
  - key: target.cluster.bootstrap.servers
    status: translated
    emit: {kind: setting, side: destination, name: servers, layer: fallback}
    diff: identity
  - key: target.cluster.security.protocol
    status: translated
    emit: {kind: tls-from-protocol, side: destination, layer: fallback}
    diff: identity
  - key: target.cluster.sasl.mechanism
    status: translated
    emit: {kind: setting, side: destination, name: saslMechanism, layer: fallback}
    diff: identity
  - key: target.cluster.sasl.jaas.config
    status: translated
    emit: {kind: sasl-jaas, side: destination, layer: fallback}
    secret: true
    diff: secret
`

func mirrorRun(t *testing.T, worker, connector string) Result {
	t.Helper()
	fs := fstest.MapFS{"mirror.yaml": {Data: []byte(testMirrorClass)}}
	for name, path := range map[string]string{
		"shared.yaml": "../rules/data/shared.yaml",
		"worker.yaml": "../rules/data/worker.yaml",
	} {
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
	req := Request{ConduitVersion: "v", RequiredSettings: BuiltinRequired, Inputs: []kcmigrate.Input{in("c.properties", "name=c\nconnector.class=com.example.Mirror\n"+connector)}}
	if worker != "" {
		w := in("worker.properties", worker)
		req.Worker = &w
	}
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// settings returns the settings of side in pipeline file path.
func settings(t *testing.T, res Result, path, side string) map[string]string {
	t.Helper()
	var f struct {
		Pipelines []struct {
			Connectors []struct {
				Type     string            `yaml:"type"`
				Settings map[string]string `yaml:"settings"`
			} `yaml:"connectors"`
		} `yaml:"pipelines"`
	}
	if err := yaml.Unmarshal([]byte(file(res, path)), &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Pipelines[0].Connectors {
		if c.Type == side {
			return c.Settings
		}
	}
	t.Fatalf("no %s in %s", side, path)
	return nil
}

func connectorOf(res Result) kcmigrate.ReportConnector {
	return res.Report.Connectors[len(res.Report.Connectors)-1]
}

// A worker key and a connector key with the same name are different keys at
// different layers: the connector's wins, whichever is applied first.
func TestLayers_ConnectorBeatsWorkerKeyOfSameName(t *testing.T) {
	is := is.New(t)
	res := mirrorRun(t, "bootstrap.servers=w:9092\nproducer.security.protocol=SASL_SSL\nproducer.sasl.mechanism=PLAIN\n"+conv,
		"source.cluster.bootstrap.servers=s:9092\ntopics=t\nproducer.sasl.mechanism=SCRAM-SHA-512\n")
	is.Equal(settings(t, res, "pipelines/c.yaml", "destination")["saslMechanism"], "SCRAM-SHA-512")
	e := entryFor(connectorOf(res), "producer.sasl.mechanism")
	is.Equal(e.Status, kcmigrate.StatusTranslated)
	is.True(e.Target != nil)
}

// A worker JAAS config under a connector override is overridden as a whole:
// no crash, the connector's credentials win, and the worker's secret gets no
// variable.
func TestLayers_WorkerJAASUnderConnectorOverride(t *testing.T) {
	is := is.New(t)
	jaas := func(u string) string {
		return `org.apache.kafka.common.security.plain.PlainLoginModule required username="` + u + `" password="p";`
	}
	res := clientRun(t, "bootstrap.servers=k:9092\nproducer.security.protocol=SASL_SSL\nproducer.sasl.jaas.config="+jaas("worker")+"\n"+conv,
		source+"producer.override.sasl.jaas.config="+jaas("conn")+"\n")
	is.Equal(settings(t, res, "pipelines/c.yaml", "destination")["saslUsername"], "conn")
	tmpl := file(res, SecretsTemplateFile)
	is.True(strings.Contains(tmpl, "PRODUCER_OVERRIDE_SASL_JAAS_CONFIG="))
	is.True(!strings.Contains(tmpl, "_PRODUCER_SASL_JAAS_CONFIG="))
}

// The fallback cluster (MirrorMaker's target.cluster.*) is used as a whole
// or not at all: once the worker sets the destination's servers, none of
// its settings are written, every one of its entries says it is not used,
// and its secret gets no variable.
func TestLayers_FallbackIsAllOrNothing(t *testing.T) {
	is := is.New(t)
	connector := "source.cluster.bootstrap.servers=s:9092\ntopics=t\n" +
		"target.cluster.bootstrap.servers=t:9092\ntarget.cluster.security.protocol=SASL_SSL\ntarget.cluster.sasl.mechanism=PLAIN\n" +
		`target.cluster.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="tu" password="tp";` + "\n"

	res := mirrorRun(t, "bootstrap.servers=w:9092\n"+conv, connector)
	dst := settings(t, res, "pipelines/c.yaml", "destination")
	is.Equal(dst["servers"], "w:9092")
	for _, k := range []string{"saslMechanism", "saslUsername", "saslPassword"} {
		_, ok := dst[k]
		is.True(!ok) // no target.cluster.* credential mixed into the worker's cluster
	}
	for _, k := range []string{"target.cluster.bootstrap.servers", "target.cluster.security.protocol", "target.cluster.sasl.mechanism", "target.cluster.sasl.jaas.config"} {
		e := entryFor(connectorOf(res), k)
		is.True(e.Target == nil)
		is.True(strings.Contains(e.Reason, "bootstrap.servers"))
	}
	is.True(!strings.Contains(file(res, SecretsTemplateFile), "TARGET_CLUSTER"))

	// Without a worker, the fallback cluster is the destination.
	res = mirrorRun(t, "", connector)
	dst = settings(t, res, "pipelines/c.yaml", "destination")
	is.Equal(dst["servers"], "t:9092")
	is.Equal(dst["saslMechanism"], "PLAIN")
	is.Equal(dst["saslUsername"], "tu")
	is.True(entryFor(connectorOf(res), "target.cluster.bootstrap.servers").Target != nil)
}

// A Kafka Connect source's consumer-side pipeline connector is the
// connector's own client: the worker's consumer.* keys and its
// bootstrap.servers never reach it, so a missing source cluster is a
// blocking required setting, not the worker's cluster.
func TestLayers_WorkerReachesOnlyConnectsOwnClient(t *testing.T) {
	is := is.New(t)
	res := mirrorRun(t, "bootstrap.servers=w:9092\nconsumer.bootstrap.servers=wc:9092\nconsumer.security.protocol=SASL_SSL\nconsumer.sasl.mechanism=PLAIN\n"+conv, "topics=t\n")
	src := settings(t, res, "pipelines/c.yaml", "source")
	is.Equal(src["servers"], "${KC_C_SOURCE_SERVERS}")
	for _, k := range []string{"tls.enabled", "saslMechanism"} {
		_, ok := src[k]
		is.True(!ok)
	}
	is.Equal(settings(t, res, "pipelines/c.yaml", "destination")["servers"], "w:9092")
	var required bool
	for _, n := range connectorOf(res).Notes {
		if n.Rule == "required/source/servers" {
			required = n.Blocking
			is.True(!strings.Contains(n.Fix, "--worker")) // the worker cannot hold the source cluster
		}
	}
	is.True(required)
	is.Equal(connectorOf(res).PipelineStatus, kcmigrate.PipelineStopped)
}

// A blocked key still owns what it would have set: Kafka Connect uses its
// value, so no lower layer may fill the setting in its place, even under
// --accept-gaps. The setting stays unset and the blocking entry stands.
func TestLayers_BlockedKeyOwnsItsSettings(t *testing.T) {
	is := is.New(t)
	login := `org.apache.kafka.common.security.plain.PlainLoginModule required username="worker" password="WPCANARY";`
	res := clientRun(t, "bootstrap.servers=k:9092\nproducer.sasl.mechanism=PLAIN\nproducer.sasl.jaas.config="+login+"\n"+conv,
		source+"producer.override.sasl.jaas.config=${env:MY_PW}\nproducer.override.sasl.mechanism=GSSAPI\n")
	is.True(entryFor(res.Report.Connectors[1], "producer.override.sasl.jaas.config").Blocking)
	is.True(entryFor(res.Report.Connectors[1], "producer.override.sasl.mechanism").Blocking)
	dst := settings(t, res, "pipelines/c.yaml", "destination")
	for _, k := range []string{"saslUsername", "saslPassword", "saslMechanism"} {
		_, ok := dst[k]
		is.True(!ok) // not the worker's login
	}
	is.True(!strings.Contains(file(res, SecretsTemplateFile), "_PRODUCER_SASL_JAAS_CONFIG="))

	// A blocked key also removes what a lower layer of the same config
	// wrote before it.
	res = mirrorRun(t, "", "source.cluster.bootstrap.servers=s:9092\ntopics=t\nproducer.sasl.mechanism=PLAIN\nx.sasl.mechanism=GSSAPI\n")
	_, ok := settings(t, res, "pipelines/c.yaml", "destination")["saslMechanism"]
	is.True(!ok)
	is.True(entryFor(connectorOf(res), "producer.sasl.mechanism").Target == nil)

	// A required setting a blocked key claims still gets its variable, so
	// the pipeline validates.
	res = mirrorRun(t, "", "source.cluster.bootstrap.servers=s:9092\ntopics=a,,b\n")
	is.True(entryFor(connectorOf(res), "topics").Blocking)
	is.Equal(settings(t, res, "pipelines/c.yaml", "source")["topics"], "${KC_C_SOURCE_TOPICS}")
}

// The plaintext note follows the input, not the pipeline: a literal secret
// in a key that ends up unused is still in the input file.
func TestLayers_PlaintextNoteFollowsInput(t *testing.T) {
	is := is.New(t)
	hasNote := func(rc kcmigrate.ReportConnector) bool {
		for _, n := range rc.Notes {
			if n.Code == kcmigrate.CodeSecretPlaintext.Reason() {
				return true
			}
		}
		return false
	}
	jaas := `target.cluster.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="t" password="TCANARY";` + "\n"
	res := mirrorRun(t, "bootstrap.servers=w:9092\n"+conv, "source.cluster.bootstrap.servers=s:9092\ntopics=t\n"+jaas)
	is.True(entryFor(connectorOf(res), "target.cluster.sasl.jaas.config").Target == nil) // unused
	is.True(hasNote(connectorOf(res)))

	// A literal in the worker file is noted on the worker, also when every
	// pipeline overrides it.
	login := `org.apache.kafka.common.security.plain.PlainLoginModule required username="w" password="WCANARY";`
	res = clientRun(t, "bootstrap.servers=k:9092\nproducer.sasl.jaas.config="+login+"\n"+conv,
		source+`producer.override.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="c" password="${env:MY_PW}";`+"\n")
	is.True(hasNote(res.Report.Connectors[0]))

	res = clientRun(t, "bootstrap.servers=k:9092\n"+conv, source+"producer.override.sasl.jaas.config=${env:MY_PW}\n")
	is.True(!hasNote(res.Report.Connectors[0]))
	is.True(!hasNote(res.Report.Connectors[1])) // a reference is not a literal
}
