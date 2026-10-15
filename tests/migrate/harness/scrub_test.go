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

package harness

import (
	"strings"
	"testing"
)

func scrubOne(key, value string) []ScrubFinding {
	return Scrub([]InputFile{{Path: "input/x.json", Keys: []KeyOccurrence{{Key: key, Value: value, Pointer: "/" + key}}}})
}

func TestScrub_Rejects(t *testing.T) {
	tests := map[string][2]string{
		"plaintext password":         {"database.password", "dbz"},
		"plaintext connection pw":    {"connection.password", "postgrespw"},
		"jaas password":              {"sasl.jaas.config", `org.apache.kafka.common.security.scram.ScramLoginModule required username="u" password="hunter2";`},
		"jdbc url password":          {"connection.url", "jdbc:postgresql://db.internal.example:5432/x?user=u&password=pw"},
		"secret key suffix":          {"s3.sse.customer.key", "abc"},
		"aws access key id in value": {"aws.access.key.id", "AKIAABCDEFGHIJKLMNOP"},
		"pem block":                  {"ssl.truststore.certificates", "-----BEGIN CERTIFICATE-----"},
		"real hostname":              {"database.hostname", "postgres"},
		"real fqdn in url":           {"connection.url", "http://elastic.corp.acme.com:9200"},
		"host port token":            {"bootstrap.servers", "kafka.internal.example:9092,broker-2.acme.io:9092"},
		"localhost":                  {"bootstrap.servers", "localhost:9092"},
		"private ip":                 {"database.hostname", "10.0.0.12"},
		"jdbc host":                  {"connection.url", "jdbc:mysql://mysql:3306/demo"},
		"userinfo url":               {"store.url", "https://user@s3.acme.com"},
		"example. prefix domain":     {"database.hostname", "example.prod.acme.com"},
		"userinfo password":          {"connection.uri", "mongodb://admin:hunter2@mongo.internal.example:27017"},
		"bare fqdn in non-host key":  {"topic.prefix", "db1.acme.io"},
		"canary with suffix":         {"database.password", "PLACEHOLDER_SECRET_DB hunter2"},
		"canary with prefix":         {"database.password", "xPLACEHOLDER_SECRET_DB"},
		"reference plus literal":     {"database.password", "${env:DB_PASSWORD}hunter2"},
		"lowercase canary":           {"database.password", "PLACEHOLDER_SECRET_db"},
		"numeric secret":             {"sasl.password", "12345678"},
		"cctld host":                 {"topic.prefix", "prod-db.acme.ca"},
		"xyz host":                   {"connection.url", "kafka.acme.xyz:9092"},
		"info host":                  {"note", "db.acme.info"},
		"uppercase host":             {"database.hostname", "PROD-DB.ACME.COM"},
		"url query token":            {"connection.url", "https://es.internal.example:9200/?token=abc123"},
	}
	for name, kv := range tests {
		t.Run(name, func(t *testing.T) {
			if f := scrubOne(kv[0], kv[1]); len(f) == 0 {
				t.Fatalf("scrub-v1 accepted %s=%s", kv[0], kv[1])
			}
		})
	}
}

func TestScrub_Accepts(t *testing.T) {
	tests := map[string][2]string{
		"canary password":       {"database.password", "PLACEHOLDER_SECRET_DB_PASSWORD"},
		"provider reference":    {"database.password", "${file:/secrets/mysql.properties:password}"},
		"empty secret":          {"connection.password", ""},
		"jaas canary":           {"sasl.jaas.config", `org.apache.kafka.common.security.scram.ScramLoginModule required username="example-user" password="PLACEHOLDER_SECRET_SASL_PASSWORD" ;`},
		"jdbc url canary":       {"connection.url", "jdbc:postgresql://postgres.internal.example:5432/inventory?user=u&password=PLACEHOLDER_SECRET_DB_PASSWORD"},
		"internal example host": {"database.hostname", "postgres.internal.example"},
		"example.com url":       {"connection.url", "https://es.example.com:9243"},
		"doc ipv4":              {"database.hostname", "192.0.2.10"},
		"doc ipv6 url":          {"connection.url", "http://[2001:db8::1]:9200"},
		"bootstrap list":        {"A.bootstrap.servers", "a-host1.internal.example:9092, a-host2.internal.example:9092"},
		"regex value":           {"transforms.route.regex", `([^.]+)\.([^.]+)\.([^.]+)`},
		"class name":            {"connector.class", "io.debezium.connector.postgresql.PostgresConnector"},
		"path format":           {"path.format", "'year'=YYYY/'month'=MM/'day'=dd"},
		"key converter":         {"key.converter", "io.confluent.connect.avro.AvroConverter"},
		"secret-free key field": {"transforms.key.field", "id"},
		"userinfo canary":       {"connection.uri", "mongodb://admin:PLACEHOLDER_SECRET_MONGO@mongo.internal.example:27017"},
		"example.com subdomain": {"connection.url", "https://es.eu.example.org:9243"},
		"java class name":       {"transforms.unwrap.type", "io.debezium.transforms.ExtractNewRecordState"},
		"file reference path":   {"database.user", "${file:/secrets/mysql.properties:user}"},
	}
	for name, kv := range tests {
		t.Run(name, func(t *testing.T) {
			if f := scrubOne(kv[0], kv[1]); len(f) != 0 {
				t.Fatalf("scrub-v1 rejected %s: %v", kv[0], f)
			}
		})
	}
}

// A finding names the key and location, never the secret value.
func TestScrub_FindingDoesNotEchoSecret(t *testing.T) {
	f := scrubOne("database.password", "s3cr3t-value")
	if len(f) != 1 {
		t.Fatalf("want one finding, got %v", f)
	}
	if strings.Contains(f[0].String(), "s3cr3t-value") {
		t.Fatalf("finding echoes the secret: %s", f[0])
	}
}

// TestScrubRaw_CatchesWhatParsingMisses covers the reviewer's misses of the
// parsed-value check: text the parsers never return as a scalar value.
func TestScrubRaw_CatchesWhatParsingMisses(t *testing.T) {
	tests := map[string]struct{ name, in string }{
		"properties comment host":   {"w.properties", "# primary is prod-db.acme.com:5432\na=1\n"},
		"properties comment url":    {"w.properties", "# see http://wiki.corp.acme.com/kafka\n"},
		"properties comment pw":     {"w.properties", "# old: password=hunter2\n"},
		"nested json value":         {"c.json", `{"connector.class":"X","o":{"url":"http://db.acme.com"}}`},
		"json array value":          {"c.json", `{"connector.class":"X","hosts":["kafka.acme.io:9092"]}`},
		"rest export task trace":    {"r.json", `{"c":{"info":{"config":{"connector.class":"X"}},"status":{"tasks":[{"trace":"SQLException: jdbc:postgresql://prod.acme.com:5432/db"}]}}}`},
		"rest export status worker": {"r.json", `{"c":{"info":{"config":{"connector.class":"X"}},"status":{"connector":{"worker_id":"10.1.2.3:8083"}}}}`},
		"bare fqdn non-host key":    {"c.json", `{"connector.class":"X","note":"talk to kafka.prod.corp"}`},
		"aws key id in comment":     {"w.properties", "# AKIAABCDEFGHIJKLMNOP\n"},
		"pem block nested":          {"c.json", `{"connector.class":"X","o":{"ca":"-----BEGIN CERTIFICATE-----"}}`},
		"userinfo in comment":       {"w.properties", "# was mongodb://admin:hunter2@mongo.internal.example\n"},
		"example prefix in comment": {"w.properties", "# moved to example.prod.acme.com\n"},
		"nested secret member":      {"c.json", `{"connector.class":"X","extra":{"database.password":"hunter2"}}`},
		"numeric secret member":     {"c.json", `{"connector.class":"X","sasl.password": 12345678}`},
		"nested numeric secret":     {"c.json", `{"connector.class":"X","o":{"api.token":42}}`},
		"secret in array object":    {"c.json", `{"connector.class":"X","a":[{"connection.password":"pw"}]}`},
		"comment colon password":    {"w.properties", "# password: hunter2\n"},
		"comment key=value secret":  {"w.properties", "# old ssl.key.password=changeit\n"},
		"uppercase host comment":    {"w.properties", "# primary PROD-DB.ACME.COM\n"},
		"uppercase host nested":     {"c.json", `{"connector.class":"X","o":{"h":"PROD-DB.ACME.COM"}}`},
		"uppercase host trace":      {"r.json", `{"c":{"info":{"config":{"connector.class":"X"}},"status":{"tasks":[{"trace":"connect to PROD-DB.ACME.COM failed"}]}}}`},
		"cctld host comment":        {"w.properties", "# replica prod-db.acme.ca\n"},
		"xyz host raw":              {"c.json", `{"connector.class":"X","o":{"h":"kafka.acme.xyz"}}`},
		"url query token raw":       {"w.properties", "# GET https://api.internal.example/v1?token=abc123\n"},
		"multiline json member":     {"c.json", "{\"connector.class\": \"X\",\n  \"extra\": {\n    \"database.password\":\n      \"hunter2\"\n  }\n}\n"},
		"multiline numeric secret":  {"c.json", "{\"connector.class\": \"X\",\n  \"sasl.password\":\n    12345678}\n"},
		"escaped json config":       {"c.json", `{"connector.class":"X","embedded":"{\"database.password\":\"hunter2\"}"}`},
		"escaped json numeric":      {"c.json", `{"connector.class":"X","embedded":"{\"api.token\": 42}"}`},
		"mixed comment line":        {"w.properties", "# password=PLACEHOLDER_SECRET_DB token: hunter2\n"},
		"topic name with port":      {"c.json", `{"connector.class":"X","bootstrap.servers":"prod.checkpoints.internal:9092"}`},
		"topic name in jdbc url":    {"c.json", `{"connector.class":"X","connection.url":"jdbc:postgresql://mm2-offsets.prod.internal:5432/db"}`},
		"topic name in value":       {"c.json", `{"connector.class":"X","database.hostname":"prod.checkpoints.internal"}`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if f := ScrubRaw(tt.name, []byte(tt.in)); len(f) == 0 {
				t.Fatalf("raw scan accepted %q", tt.in)
			}
		})
	}
}

func TestScrubRaw_Accepts(t *testing.T) {
	tests := map[string]string{
		"asf license header":  "# Licensed to the Apache Software Foundation (ASF).\n#    http://www.apache.org/licenses/LICENSE-2.0\n",
		"jaas canary json":    `{"sasl.jaas.config":"ScramLoginModule required username=\"example-user\" password=\"PLACEHOLDER_SECRET_SASL_PASSWORD\" ;"}`,
		"topic names":         "# mm2-offsets.B.internal is the offsets topic\n",
		"bind all":            "# Specify hostname as 0.0.0.0 to bind to all interfaces.\n",
		"class and files":     `{"connector.class":"io.confluent.connect.s3.S3SinkConnector","x":"${file:/secrets/mysql.properties:password}"}`,
		"mm2 internal topics": "# mm2-offsets.B.internal, B.checkpoints.internal, MM2-CONFIGS.B.INTERNAL\n",
		"config key names":    `{"database.server.id": "184054", "partition.duration.ms": 1800000, "plugin.name": "pgoutput"}`,
		"properties keys":     "group.id=connect-cluster\noffset.flush.interval.ms=10000\n",
		"placeholder members": `{"o":{"database.password":"PLACEHOLDER_SECRET_DB_PASSWORD","sasl.password":"${env:SASL}"}}`,
		"canary query token":  "# https://api.internal.example/v1?token=PLACEHOLDER_SECRET_API_TOKEN\n",
		"escaped canary":      `{"embedded":"{\"database.password\":\"PLACEHOLDER_SECRET_DB\"}"}`,
		"multiline canary":    "{\n  \"database.password\":\n    \"PLACEHOLDER_SECRET_DB\"\n}\n",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if f := ScrubRaw("x", []byte(in)); len(f) != 0 {
				t.Fatalf("raw scan rejected: %v", f)
			}
		})
	}
}
