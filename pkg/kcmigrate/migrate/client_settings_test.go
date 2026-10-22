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
	"github.com/matryer/is"
)

// These tests pin how Kafka Connect builds a connector's Kafka clients
// (Worker.baseProducerConfigs and baseConsumerConfigs, Apache Kafka 4.2.0,
// connect/runtime/.../Worker.java:837-874 and 911-942): the worker's bootstrap.servers,
// then the worker's producer.- or consumer.-prefixed keys, then the
// connector's producer.override. or consumer.override. keys, later ones
// winning. The worker's own top-level security.protocol, sasl.* and ssl.*
// configure the worker's internal clients and never reach a connector's.

const testSinkClass = `rulesVersion: 1
scope: sink
class: com.example.ExampleSink
upstream: {name: example, version: "1.0"}
emits:
  - {plugin: builtin:kafka, role: source}
  - {plugin: builtin:log, role: destination}
keys: []
`

func clientEngine(t *testing.T) *rules.Engine {
	t.Helper()
	fs := fstest.MapFS{"sink.yaml": {Data: []byte(testSinkClass)}}
	for name, path := range map[string]string{
		"shared.yaml":  "../rules/data/shared.yaml",
		"worker.yaml":  "../rules/data/worker.yaml",
		"example.yaml": "../rules/testdata/valid/example-source.yaml",
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
	return e
}

const conv = "key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n"

func clientRun(t *testing.T, worker, connector string) Result {
	t.Helper()
	req := Request{ConduitVersion: "v", Inputs: []kcmigrate.Input{in("c.properties", connector)}}
	if worker != "" {
		w := in("worker.properties", worker)
		req.Worker = &w
	}
	res, err := Run(context.Background(), clientEngine(t), withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

const (
	source = "name=c\nconnector.class=com.example.ExampleSource\n"
	sink   = "name=c\nconnector.class=com.example.ExampleSink\n"
)

func TestClient_OverrideBootstrapServers(t *testing.T) {
	is := is.New(t)
	res := clientRun(t, "bootstrap.servers=internal:9092\n"+conv, source+"producer.override.bootstrap.servers=other:9092\n")
	e := entryFor(res.Report.Connectors[1], "producer.override.bootstrap.servers")
	is.Equal(e.Status, kcmigrate.StatusTranslated)
	p := file(res, "pipelines/c.yaml")
	is.True(strings.Contains(p, "servers: other:9092"))
	is.True(!strings.Contains(p, "internal:9092"))

	res = clientRun(t, "bootstrap.servers=internal:9092\n"+conv, sink+"topics=t\nconsumer.override.bootstrap.servers=other:9092\n")
	is.True(strings.Contains(file(res, "pipelines/c.yaml"), "servers: other:9092"))
}

func TestClient_WorkerTopLevelSecurityDoesNotReachConnectors(t *testing.T) {
	is := is.New(t)
	worker := "bootstrap.servers=k:9092\nsecurity.protocol=SASL_SSL\nsasl.mechanism=GSSAPI\nsasl.jaas.config=com.sun.security.auth.module.Krb5LoginModule required;\nssl.truststore.location=/t.jks\n" + conv
	res := clientRun(t, worker, source)
	for _, e := range res.Report.Connectors[0].Entries {
		is.Equal(e.Status, kcmigrate.StatusTranslated) // the worker's own clients
		is.True(!e.Blocking)
	}
	p := file(res, "pipelines/c.yaml")
	is.True(!strings.Contains(p, "tls.enabled"))
	is.True(!strings.Contains(p, "sasl"))
}

func TestClient_WorkerPrefixedClientSettings(t *testing.T) {
	is := is.New(t)
	worker := "bootstrap.servers=internal:9092\nproducer.bootstrap.servers=data:9092\nproducer.security.protocol=SASL_SSL\nproducer.sasl.mechanism=SCRAM-SHA-512\n" +
		"producer.sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required username=\"u\" password=\"CANARY\";\n" + conv
	res := clientRun(t, worker, source)
	p := file(res, "pipelines/c.yaml")
	for _, want := range []string{"servers: data:9092", `tls.enabled: "true"`, "saslMechanism: SCRAM-SHA-512", "saslUsername: u", "saslPassword: ${"} {
		is.True(strings.Contains(p, want))
	}
	is.True(!strings.Contains(p, "internal:9092")) // producer.bootstrap.servers wins over bootstrap.servers
	for _, f := range res.Files {
		is.True(!strings.Contains(string(f.Content), "CANARY"))
	}
	// A sink's consumer does not get the producer's settings.
	res = clientRun(t, worker, sink+"topics=t\n")
	p = file(res, "pipelines/c.yaml")
	is.True(strings.Contains(p, "servers: internal:9092"))
	is.True(!strings.Contains(p, "sasl"))
}

func TestClient_ConnectorPlaintextBeatsWorkerTLS(t *testing.T) {
	is := is.New(t)
	res := clientRun(t, "bootstrap.servers=k:9092\nproducer.security.protocol=SSL\n"+conv, source+"producer.override.security.protocol=PLAINTEXT\n")
	is.True(strings.Contains(file(res, "pipelines/c.yaml"), `tls.enabled: "false"`))
}

func TestClient_JAASThatCannotBeSplitBlocks(t *testing.T) {
	for name, jaas := range map[string]string{
		"escaped quote": `org.apache.kafka.common.security.scram.ScramLoginModule required username="ab\"cd" password="p\"w";`,
		"extra option":  `org.apache.kafka.common.security.scram.ScramLoginModule required username="u" password="p" tokenauth="true";`,
		"two modules":   `org.apache.kafka.common.security.plain.PlainLoginModule required username="u" password="p"; org.apache.kafka.common.security.plain.PlainLoginModule required username="v" password="q";`,
		"optional flag": `org.apache.kafka.common.security.plain.PlainLoginModule optional username="u" password="p";`,
		"no semicolon":  `org.apache.kafka.common.security.plain.PlainLoginModule required username="u" password="p"`,
		"kerberos":      `com.sun.security.auth.module.Krb5LoginModule required useKeyTab=true;`,
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			res := clientRun(t, "bootstrap.servers=k:9092\n"+conv, source+"producer.override.sasl.jaas.config="+jaas+"\n")
			e := entryFor(res.Report.Connectors[1], "producer.override.sasl.jaas.config")
			is.True(e.Blocking)
			is.True(!strings.Contains(file(res, "pipelines/c.yaml"), "saslUsername"))
		})
	}
	is := is.New(t)
	res := clientRun(t, "bootstrap.servers=k:9092\n"+conv, source+"producer.override.security.protocol=SASL_SSL\n"+`producer.override.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required  username = svc password="p w" ;`+"\n")
	is.True(!entryFor(res.Report.Connectors[1], "producer.override.sasl.jaas.config").Blocking)
	is.True(strings.Contains(file(res, "pipelines/c.yaml"), "saslUsername: svc"))
}

func TestClient_TopicsListIsTrimmedAndSplit(t *testing.T) {
	is := is.New(t)
	res := clientRun(t, "bootstrap.servers=k:9092\n"+conv, sink+"topics= orders, payments \n")
	is.True(strings.Contains(file(res, "pipelines/c.yaml"), "topics: orders,payments\n"))
	res = clientRun(t, "bootstrap.servers=k:9092\n"+conv, sink+"topics=orders,,payments\n")
	is.True(entryFor(res.Report.Connectors[1], "topics").Blocking) // Kafka keeps the empty name
}

func TestClient_WorkerPrefixedStoresBlock(t *testing.T) {
	is := is.New(t)
	res := clientRun(t, "bootstrap.servers=k:9092\nproducer.ssl.keystore.location=/k.jks\n"+conv, source)
	is.True(entryFor(res.Report.Connectors[0], "producer.ssl.keystore.location").Blocking)
	is.Equal(res.Report.Connectors[1].PipelineStatus, kcmigrate.PipelineStopped)
}

// The design's secret list applies to every entry, also under a class migrate
// does not know: the report marks connection.password secret.
func TestClient_SecretFlagUnderUnknownClass(t *testing.T) {
	is := is.New(t)
	res := clientRun(t, "", "name=c\nconnector.class=io.debezium.connector.jdbc.JdbcSinkConnector\nconnection.password=CANARY\nconnection.url=jdbc:x\n")
	is.True(entryFor(res.Report.Connectors[0], "connection.password").Secret)
	is.True(!entryFor(res.Report.Connectors[0], "connection.url").Secret)
}
