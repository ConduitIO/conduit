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
