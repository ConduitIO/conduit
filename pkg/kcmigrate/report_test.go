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

package kcmigrate

import (
	"bytes"
	"flag"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

var update = flag.Bool("update", false, "rewrite golden files")

const exampleGolden = "testdata/report/example.report.json"

// exampleReport sets every field of the contract at least once. The golden
// file it produces is the reference example for consumers (the migration
// harness, docs) and is what the schema-coverage test walks.
func exampleReport() *Report {
	r := NewReport("v0.21.0")
	r.Generator.Rules = []RulesInfo{{
		Class:        "io.debezium.connector.postgresql.PostgresConnector",
		RulesVersion: 1,
		Upstream:     Upstream{Name: "debezium", Version: "3.7.0.Final"},
	}}
	r.Connectors = []ReportConnector{
		{
			Name: "connect-distributed.properties", Class: "", Role: RoleWorker,
			Entries: []Entry{{
				Key: "group.id", Status: StatusTranslated,
				Origin: Origin{File: "connect-distributed.properties", Line: 2, Column: 1},
				Reason: "Conduit has no worker cluster.",
			}},
		},
		{
			Name:           "orders-cdc",
			Class:          "io.debezium.connector.postgresql.PostgresConnector",
			Role:           RoleSource,
			Pipeline:       "pipelines/orders-cdc.yaml",
			PipelineStatus: PipelineStopped,
			Target: &Target{
				Source: "builtin:postgres", Destination: "builtin:kafka",
				Certification: map[string]string{"builtin:postgres": "certified", "builtin:kafka": "tested"},
			},
			Entries: []Entry{
				{
					Key: "database.password", Status: StatusTranslated, Secret: true,
					Origin: Origin{File: "orders.json", Pointer: "/config/database.password"},
					Target: &EntryTarget{ConfigPath: "/pipelines/0/connectors/0/settings/url", Env: "KC_ORDERS_CDC_DATABASE_PASSWORD"},
				},
				{
					Key: "slot.name", Status: StatusApproximated, Code: "kc.key_approximated",
					Origin: Origin{File: "orders.json", Pointer: "/config/slot.name"},
					Target: &EntryTarget{ConfigPath: "/pipelines/0/connectors/0/settings/logrepl.slotName"},
					Reason: `Conduit uses its own slot "conduit_orders_cdc" so rollback stays possible.`,
				},
				{
					Key: "decimal.handling.mode", Status: StatusUnsupported, Code: "kc.key_unsupported", Blocking: true,
					Origin: Origin{File: "orders.json", Pointer: "/config/decimal.handling.mode"},
					Reason: `"double" is not an encoding Debezium-compatible output mode supports.`,
					Fix:    `Use "precise" or "string", or keep this connector on Kafka Connect.`,
				},
				{
					Key: "transforms.route.type", Status: StatusTranslated,
					Origin: Origin{File: "orders.json", Pointer: "/config/transforms.route.type"},
					Target: &EntryTarget{ConfigPath: "/pipelines/0/connectors/0/processors/0"},
				},
			},
			Notes: []Note{{
				Code: "kc.default_differs", Blocking: true, Rule: "debezium-postgres/defaults/plugin.name",
				Reason: "plugin.name is absent, so Debezium uses decoderbufs; decoder differences are not emulated.",
				Fix:    "Set plugin.name=pgoutput in the Kafka Connect config, or accept the difference.",
			}},
			Transforms: []Transform{{
				Alias: "route", Class: "org.apache.kafka.connect.transforms.RegexRouter",
				Status: StatusTranslated, Processor: "field.set",
			}},
			Resume: &Resume{
				Strategy: "fresh", Snapshot: "skipped", Preflights: "pending",
				Import: &ResumeImport{
					Setting: "logrepl.import.debeziumOffset", Source: "topic", Topic: "connect-offsets",
					Key: `["orders-cdc",{"server":"orders"}]`,
				},
				Notes: []string{"The no-re-snapshot claim holds only if the connector preflights pass."},
			},
			OutputCompat: &OutputCompat{
				Mode: "debezium", Compat: "3.7",
				Converters:      Converters{Key: "io.confluent.connect.avro.AvroConverter", Value: "io.confluent.connect.avro.AvroConverter", Header: "org.apache.kafka.connect.storage.SimpleHeaderConverter"},
				SubjectStrategy: "TopicNameStrategy",
				Differences:     []string{"D1", "D2"},
			},
		},
		{
			Name: "custom", Class: "com.example.CustomConnector", Role: RoleUnknown,
			Entries: []Entry{{
				Key: "connector.class", Status: StatusUnsupported, Code: "kc.connector_class_unsupported", Blocking: true,
				Origin: Origin{File: "custom.properties", Line: 1, Column: 1},
				Reason: "com.example.CustomConnector has no Conduit mapping.",
				Fix:    "Keep this connector on Kafka Connect.",
			}},
		},
	}
	r.AcceptedGaps = []AcceptedGap{
		{Connector: "orders-cdc", Code: "kc.key_unsupported", Key: "decimal.handling.mode", Origin: &Origin{File: "orders.json", Pointer: "/config/decimal.handling.mode"}},
		{Connector: "orders-cdc", Code: "kc.default_differs"},
	}
	r.Summarize()
	return r
}

func TestReport_ExampleGolden(t *testing.T) {
	is := is.New(t)
	r := exampleReport()
	is.NoErr(r.Validate())
	got, err := MarshalReport(r)
	is.NoErr(err)
	is.NoErr(schema.ValidateReport(got))

	if *update {
		is.NoErr(os.MkdirAll("testdata/report", 0o755))
		is.NoErr(os.WriteFile(exampleGolden, got, 0o600))
	}
	want, err := os.ReadFile(exampleGolden)
	is.NoErr(err)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run go test ./pkg/kcmigrate -run TestReport_ExampleGolden -update and review the diff", exampleGolden)
	}
	is.NoErr(schema.ValidateReport(want))
}

func TestReport_Summarize(t *testing.T) {
	is := is.New(t)
	r := exampleReport()
	is.Equal(r.Summary, Summary{Connectors: 3, Keys: 6, Translated: 3, Approximated: 1, Unsupported: 2, Blocking: 3})
	is.True(!r.OK())

	empty := NewReport("v0.21.0")
	empty.Summarize()
	is.True(empty.OK())
	b, err := MarshalReport(empty)
	is.NoErr(err)
	is.NoErr(schema.ValidateReport(b))
}

func TestReport_MarshalIsDeterministicAndNeverNull(t *testing.T) {
	is := is.New(t)
	r := exampleReport()
	r.Connectors[0].Notes, r.Connectors[0].Transforms = nil, nil
	r.Generator.Rules, r.AcceptedGaps = nil, nil
	first, err := MarshalReport(r)
	is.NoErr(err)
	for range 20 {
		again, err := MarshalReport(r)
		is.NoErr(err)
		is.Equal(first, again)
	}
	is.True(!bytes.Contains(first, []byte("null")))
	is.True(!bytes.Contains(first, []byte(`<`))) // no HTML escaping
	is.NoErr(schema.ValidateReport(first))
	is.Equal(r.Connectors[0].Notes, []Note(nil)) // the input report is not modified
}

// TestReport_ValidateRejects pins the producer rules Validate enforces. The
// schema expresses the same rules (TestSchema_Rejects).
func TestReport_ValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(r *Report)
		want   string
	}{
		{"approximated without reason", func(r *Report) { r.Connectors[1].Entries[1].Reason = "" }, "no reason"},
		{"unsupported without code", func(r *Report) { r.Connectors[1].Entries[2].Code = "" }, "no code"},
		{"unsupported without fix", func(r *Report) { r.Connectors[1].Entries[2].Fix = "" }, "no fix"},
		{"blocking translated without fix", func(r *Report) { r.Connectors[1].Entries[3].Blocking = true; r.Summarize() }, "no fix"},
		{"unknown status", func(r *Report) { r.Connectors[1].Entries[0].Status = "dropped" }, "unknown status"},
		{"blocking note without fix", func(r *Report) { r.Connectors[1].Notes[0].Fix = "" }, "has no fix"},
		{"note without rule", func(r *Report) { r.Connectors[1].Notes[0].Rule = "" }, "lacks code, reason or rule"},
		{"stale summary", func(r *Report) { r.Summary.Keys++ }, "does not match"},
		{"pipeline without status", func(r *Report) { r.Connectors[1].PipelineStatus = "" }, "set together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			r := exampleReport()
			tt.mutate(r)
			err := r.Validate()
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), tt.want))
		})
	}
}

func TestSchema_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(doc map[string]any)
	}{
		{"wrong reportVersion", func(d map[string]any) { d["reportVersion"] = 2 }},
		{"unknown top-level field", func(d map[string]any) { d["extra"] = true }},
		{"missing acceptedGaps", func(d map[string]any) { delete(d, "acceptedGaps") }},
		{"null entries", func(d map[string]any) { conn(d, 1)["entries"] = nil }},
		{"unknown status", func(d map[string]any) { entry(d, 1, 0)["status"] = "dropped" }},
		{"approximated without reason", func(d map[string]any) { delete(entry(d, 1, 1), "reason") }},
		{"unsupported without fix", func(d map[string]any) { delete(entry(d, 1, 2), "fix") }},
		{"blocking without fix", func(d map[string]any) { e := entry(d, 1, 3); e["blocking"] = true }},
		{"origin with line and pointer", func(d map[string]any) { entry(d, 1, 0)["origin"].(map[string]any)["line"] = 1 }},
		{"origin with neither", func(d map[string]any) { delete(entry(d, 1, 0)["origin"].(map[string]any), "pointer") }},
		{"bad code", func(d map[string]any) { entry(d, 1, 2)["code"] = "KeyUnsupported" }},
		{"pipeline without status", func(d map[string]any) { delete(conn(d, 1), "pipelineStatus") }},
		{"blocking note without fix", func(d map[string]any) { delete(conn(d, 1)["notes"].([]any)[0].(map[string]any), "fix") }},
		{"secret false written", func(d map[string]any) { entry(d, 1, 0)["secret"] = false }},
		{"value field on entry", func(d map[string]any) { entry(d, 1, 0)["value"] = "x" }},
	}
	base, err := MarshalReport(exampleReport())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			var doc map[string]any
			is.NoErr(json.Unmarshal(base, &doc))
			tt.mutate(doc)
			b, err := json.Marshal(doc)
			is.NoErr(err)
			is.True(schema.ValidateReport(b) != nil)
		})
	}
}

func conn(d map[string]any, i int) map[string]any {
	return d["connectors"].([]any)[i].(map[string]any)
}

func entry(d map[string]any, c, e int) map[string]any {
	return conn(d, c)["entries"].([]any)[e].(map[string]any)
}

// TestSchema_GoTypesCoverEveryProperty checks the direction
// additionalProperties:false cannot: every property the schema declares is
// produced by the Go types (the example sets every field), so the schema
// never documents a field the producer cannot write.
func TestSchema_GoTypesCoverEveryProperty(t *testing.T) {
	is := is.New(t)
	var sch map[string]any
	is.NoErr(json.Unmarshal(schema.ReportV1, &sch))
	b, err := MarshalReport(exampleReport())
	is.NoErr(err)
	var inst any
	is.NoErr(json.Unmarshal(b, &inst))

	seen := map[string]map[string]bool{} // schema node path -> instance keys
	declared := map[string][]string{}    // schema node path -> declared properties
	var walk func(node map[string]any, path string, v any)
	walk = func(node map[string]any, path string, v any) {
		if ref, ok := node["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/$defs/")
			walk(sch["$defs"].(map[string]any)[name].(map[string]any), "#/$defs/"+name, v)
			return
		}
		if props, ok := node["properties"].(map[string]any); ok {
			if declared[path] == nil {
				for k := range props {
					declared[path] = append(declared[path], k)
				}
			}
			obj, _ := v.(map[string]any)
			if seen[path] == nil {
				seen[path] = map[string]bool{}
			}
			for k, pv := range obj {
				seen[path][k] = true
				if ps, ok := props[k].(map[string]any); ok {
					walk(ps, path+"/properties/"+k, pv)
				}
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			arr, _ := v.([]any)
			for _, e := range arr {
				walk(items, path+"/items", e)
			}
		}
	}
	walk(sch, "#", inst)

	var missing []string
	for path, props := range declared {
		for _, p := range props {
			if !seen[path][p] {
				missing = append(missing, path+"/properties/"+p)
			}
		}
	}
	sort.Strings(missing)
	is.Equal(missing, []string(nil)) // schema properties the example report never produced
	is.True(len(declared) >= 10)     // the walk reached the nested definitions
}

func TestOrigin_String(t *testing.T) {
	is := is.New(t)
	is.Equal(Origin{File: "a.properties", Line: 3, Column: 7}.String(), "a.properties:3:7")
	is.Equal(Origin{File: "a.json", Pointer: "/config/x"}.String(), "a.json#/config/x")
}
