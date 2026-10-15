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
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	provyaml "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"github.com/matryer/is"
)

// sharedEngine is the production shared and worker rules plus a test source
// class whose destination is builtin:kafka, so the shared rows can be run
// end to end before any production class exists.
func sharedEngine(t *testing.T) *rules.Engine {
	t.Helper()
	fs := fstest.MapFS{}
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

const sharedBase = "name=c\nconnector.class=com.example.ExampleSource\nkey.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n"

func TestSharedRules(t *testing.T) {
	tests := []struct {
		name     string
		key, val string
		status   kcmigrate.Status
		code     string
		blocking bool
		yaml     string // must appear in the pipeline file
	}{
		{"one task", "tasks.max", "1", kcmigrate.StatusTranslated, "", false, ""},
		{"many tasks", "tasks.max", "4", kcmigrate.StatusApproximated, "kc.key_approximated", false, ""},
		{"bad tasks", "tasks.max", "four", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"acks -1", "producer.override.acks", "-1", kcmigrate.StatusTranslated, "", false, "acks: all"},
		{"acks 1", "producer.override.acks", "1", kcmigrate.StatusTranslated, "", false, "acks: one"},
		{"compression", "producer.override.compression.type", "zstd", kcmigrate.StatusTranslated, "", false, "compression: zstd"},
		{"producer tuning", "producer.override.linger.ms", "5", kcmigrate.StatusUnsupported, "kc.key_unsupported", false, ""},
		{"producer keystore", "producer.override.ssl.keystore.location", "/k.jks", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"tls", "producer.override.security.protocol", "SASL_SSL", kcmigrate.StatusTranslated, "", false, `tls.enabled: "true"`},
		{"plaintext", "producer.override.security.protocol", "plaintext", kcmigrate.StatusTranslated, "", false, ""},
		{"mechanism", "producer.override.sasl.mechanism", "SCRAM-SHA-512", kcmigrate.StatusTranslated, "", false, "saslMechanism: SCRAM-SHA-512"},
		{"gssapi", "producer.override.sasl.mechanism", "GSSAPI", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"kerberos jaas", "producer.override.sasl.jaas.config", `com.sun.security.auth.module.Krb5LoginModule required useKeyTab=true;`, kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"consumer key on a source", "consumer.override.group.id", "g", kcmigrate.StatusUnsupported, "kc.key_unsupported", false, ""},
		{"topics on a source", "topics", "a,b", kcmigrate.StatusUnsupported, "kc.key_unsupported", false, ""},
		{"topics regex", "topics.regex", "a.*", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"topic creation", "topic.creation.default.partitions", "3", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"tolerance none", "errors.tolerance", "none", kcmigrate.StatusTranslated, "", false, "window-nack-threshold: 0"},
		{"tolerance all", "errors.tolerance", "all", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"dlq topic", "errors.deadletterqueue.topic.name", "dlq", kcmigrate.StatusTranslated, "", false, ""},
		{"empty topics", "topics", "", kcmigrate.StatusTranslated, "", false, ""},
		{"empty topics regex", "topics.regex", "", kcmigrate.StatusTranslated, "", false, ""},
		{"default boundary", "transaction.boundary", "poll", kcmigrate.StatusTranslated, "", false, ""},
		{"other boundary", "transaction.boundary", "connector", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"no transforms", "transforms", "", kcmigrate.StatusTranslated, "", false, ""},
		{"transforms", "transforms", "route", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"transform setting", "transforms.route.type", "org.apache.kafka.connect.transforms.RegexRouter", kcmigrate.StatusUnsupported, "kc.smt_unsupported", true, ""},
		{"exactly once required", "exactly.once.support", "required", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"exactly once requested", "exactly.once.support", "requested", kcmigrate.StatusTranslated, "", false, ""},
		{"json converter", keyConverter, "org.apache.kafka.connect.json.JsonConverter", kcmigrate.StatusUnsupported, "kc.key_unsupported", true, ""},
		{"converter setting", "value.converter.schemas.enable", "false", kcmigrate.StatusUnsupported, "kc.key_unsupported", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			content := sharedBase
			if tt.key == keyConverter {
				content = strings.Replace(content, "key.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n", "", 1)
			}
			content += tt.key + "=" + tt.val + "\n"
			res, err := Run(context.Background(), sharedEngine(t), withValidator(Request{Inputs: []kcmigrate.Input{in("c.properties", content)}, ConduitVersion: "v"}))
			is.NoErr(err)
			e := entryFor(res.Report.Connectors[0], tt.key)
			is.Equal(e.Status, tt.status)
			is.Equal(e.Code, tt.code)
			is.Equal(e.Blocking, tt.blocking)
			p := file(res, "pipelines/c.yaml")
			if tt.yaml != "" {
				is.True(strings.Contains(p, tt.yaml))
			}
			checkPipeline(t, p)
			is.NoErr(schema.ValidateReport(res.Files[0].Content))
		})
	}
}

func TestSharedRules_JAAS(t *testing.T) {
	is := is.New(t)
	jaas := `org.apache.kafka.common.security.plain.PlainLoginModule required username="svc" password="CANARY-jaas";`
	res := runOneWith(t, sharedEngine(t), Request{Inputs: []kcmigrate.Input{in("c.properties", sharedBase+"producer.override.sasl.jaas.config="+jaas+"\n")}})
	p := file(res, "pipelines/c.yaml")
	is.True(strings.Contains(p, "saslUsername: svc"))
	is.True(strings.Contains(p, "saslPassword: ${KC_C_PRODUCER_OVERRIDE_SASL_JAAS_CONFIG}"))
	for _, f := range res.Files {
		is.True(!strings.Contains(string(f.Content), "CANARY"))
	}
	e := entryFor(res.Report.Connectors[0], "producer.override.sasl.jaas.config")
	is.True(e.Secret)
	is.Equal(e.Target.Env, "KC_C_PRODUCER_OVERRIDE_SASL_JAAS_CONFIG")
	is.Equal(res.Report.Connectors[0].Notes[0].Code, "kc.secret_plaintext")
	checkPipeline(t, p)
}

func TestSharedRules_WorkerInheritance(t *testing.T) {
	is := is.New(t)
	worker := in("connect.properties", "bootstrap.servers=k1.internal.example:9092\nproducer.security.protocol=SSL\nsecurity.protocol=SASL_SSL\ngroup.id=connect\nkey.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\nrest.port=8083\n")
	res := runOneWith(t, sharedEngine(t), Request{Worker: &worker, Inputs: []kcmigrate.Input{in("c.properties", "name=c\nconnector.class=com.example.ExampleSource\n")}})
	p := file(res, "pipelines/c.yaml")
	is.True(strings.Contains(p, "servers: k1.internal.example:9092"))
	is.True(strings.Contains(p, `tls.enabled: "true"`))
	is.Equal(res.Report.Summary.Blocking, 0)
	is.True(res.OK)
	var codes []string
	for _, n := range res.Report.Connectors[1].Notes {
		codes = append(codes, n.Code+" "+n.Rule)
	}
	// The worker's converters count, so no kc.converter_unknown; the absent
	// errors.retry.timeout and header.converter defaults are noted.
	is.Equal(codes, []string{"kc.default_differs shared/defaults/errors.retry.timeout", "kc.default_differs shared/defaults/header.converter"})
	checkPipeline(t, p)
}

func runOneWith(t *testing.T, e *rules.Engine, req Request) Result {
	t.Helper()
	req.ConduitVersion = "v"
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func checkPipeline(t *testing.T, content string) {
	t.Helper()
	ps, err := provyaml.NewParser(log.Nop()).Parse(context.Background(), bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatalf("pipeline does not parse: %v\n%s", err, content)
	}
	for _, p := range ps {
		if err := config.Validate(p); err != nil {
			t.Fatalf("pipeline does not validate: %v\n%s", err, content)
		}
	}
}
