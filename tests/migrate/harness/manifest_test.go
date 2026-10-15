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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matryer/is"
)

const goodManifest = `schemaVersion: 1
id: good-case
family: jdbc-sink
description: A case.
provenance:
  source: vendor-doc
  ref: https://github.com/debezium/debezium-examples/blob/8b083bcb37986f828d4af47f11cbdaeefbae7d63/x.json
  license: Apache-2.0
  copyright: Debezium Authors
  modifications: ["scrubbed"]
sanitized: {scrubbed: true, check: scrub-v1}
command: {args: [migrate, kafka-connect, input/connector.json]}
expect:
  exitCode: 2
  entries:
    - {key: auto.create, status: unsupported, blocking: true}
covers:
  - {class: io.confluent.connect.jdbc.JdbcSinkConnector, keys: [auto.create]}
emits:
  - {plugin: builtin:kafka, role: source}
run: {tier: offline}
`

// writeCase writes a one-case corpus and returns its root.
func writeCase(t *testing.T, manifest string, inputs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "jdbc-sink", "good-case")
	if err := os.MkdirAll(filepath.Join(dir, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "case.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range inputs {
		if err := os.WriteFile(filepath.Join(dir, "input", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var oneInput = map[string]string{"connector.json": `{"connector.class":"io.confluent.connect.jdbc.JdbcSinkConnector","auto.create":"true"}`}

func TestLoadCorpus_Good(t *testing.T) {
	is := is.New(t)
	cases, problems := LoadCorpus(writeCase(t, goodManifest, oneInput))
	is.Equal(len(problems), 0)
	is.Equal(len(cases), 1)
	is.Equal(cases[0].Rel, "jdbc-sink/good-case")
	is.Equal(cases[0].Inputs(), []string{"input/connector.json"})
	is.Equal(*cases[0].Manifest.Expect.ExitCode, 2)
}

func TestLoadCorpus_Rejects(t *testing.T) {
	tests := map[string]struct {
		edit   func(string) string
		inputs map[string]string
		want   string
	}{
		"unknown field":   {edit: func(s string) string { return s + "bogus: 1\n" }, want: "bogus"},
		"id mismatch":     {edit: func(s string) string { return strings.Replace(s, "id: good-case", "id: other", 1) }, want: "id"},
		"family mismatch": {edit: func(s string) string { return strings.Replace(s, "family: jdbc-sink", "family: s3-sink", 1) }, want: "family"},
		"unpinned ref": {edit: func(s string) string {
			return strings.Replace(s, "8b083bcb37986f828d4af47f11cbdaeefbae7d63", "main", 1)
		}, want: "40-hex"},
		"license not allowed": {edit: func(s string) string { return strings.Replace(s, "Apache-2.0", "Confluent-Community-1.0", 1) }, want: "license"},
		"no modifications":    {edit: func(s string) string { return strings.Replace(s, `  modifications: ["scrubbed"]`+"\n", "", 1) }, want: "modifications"},
		"not scrubbed":        {edit: func(s string) string { return strings.Replace(s, "scrubbed: true", "scrubbed: false", 1) }, want: "sanitized"},
		"runner flag in args": {edit: func(s string) string {
			return strings.Replace(s, "input/connector.json]", "input/connector.json, --json]", 1)
		}, want: "--json"},
		"wrong command": {edit: func(s string) string { return strings.Replace(s, "[migrate, kafka-connect,", "[kc, lint,", 1) }, want: "command.args"},
		"missing input": {edit: func(s string) string { return s }, inputs: map[string]string{}, want: "does not exist"},
		"orphan input":  {edit: func(s string) string { return s }, inputs: map[string]string{"connector.json": "{}", "extra.json": "{}"}, want: "not named"},
		"bad status":    {edit: func(s string) string { return strings.Replace(s, "status: unsupported", "status: dropped", 1) }, want: "status"},
		"bad exit code": {edit: func(s string) string { return strings.Replace(s, "exitCode: 2", "exitCode: 7", 1) }, want: "exitCode"},
		"error with entries": {edit: func(s string) string {
			return strings.Replace(s, "exitCode: 2", "exitCode: 2\n  error: migrate.input_unrecognized", 1)
		}, want: "expect.error"},
		"no covers": {edit: func(s string) string {
			return strings.Replace(s, "  - {class: io.confluent.connect.jdbc.JdbcSinkConnector, keys: [auto.create]}\n", "", 1)
		}, want: "covers"},
		"bad emit role":          {edit: func(s string) string { return strings.Replace(s, "role: source", "role: sink", 1) }, want: "emits"},
		"compose without oracle": {edit: func(s string) string { return strings.Replace(s, "tier: offline", "tier: compose", 1) }, want: "oracle"},
		"yaml input":             {edit: func(s string) string { return strings.ReplaceAll(s, "connector.json", "connector.yaml") }, inputs: map[string]string{"connector.yaml": "a: b"}, want: ".json and .properties"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			inputs := tt.inputs
			if inputs == nil {
				inputs = oneInput
			}
			_, problems := LoadCorpus(writeCase(t, tt.edit(goodManifest), inputs))
			var all []string
			for _, p := range problems {
				all = append(all, p.Error())
			}
			if !strings.Contains(strings.Join(all, "\n"), tt.want) {
				t.Fatalf("want a problem mentioning %q, got %q", tt.want, all)
			}
		})
	}
}

func TestCheckCertification(t *testing.T) {
	is := is.New(t)
	entries := []CertificationEntry{
		{Plugin: "builtin:postgres", Version: ">=0.15.0", Tier: TierPending, Lane: 3004},
		{Plugin: "mysql", Version: ">=0.2.0", Tier: TierCertified, Lane: 3005},
	}
	is.Equal(len(CheckCertification(entries, []string{"builtin:postgres@v0.15.0", "standalone:mysql@v0.2.0"}, false)), 0)
	missing := CheckCertification(entries, []string{"builtin:s3", "builtin:s3@v0.9.5"}, false)
	is.Equal(len(missing), 1) // reported once per plugin
	is.True(strings.Contains(missing[0], "builtin:s3"))
	release := CheckCertification(entries, []string{"builtin:postgres", "mysql"}, true)
	is.Equal(len(release), 1)
	is.True(strings.Contains(release[0], "builtin:postgres"))
}

func TestLoadCertification_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":   `- {plugin: mysql, version: ">=0.2.0", tier: pending, lane: 1, bogus: x}`,
		"bad tier":        `- {plugin: mysql, version: ">=0.2.0", tier: verified, lane: 1}`,
		"bad version":     `- {plugin: mysql, version: "0.2", tier: pending, lane: 1}`,
		"no lane":         `- {plugin: mysql, version: ">=0.2.0", tier: pending}`,
		"duplicate":       "- {plugin: mysql, version: \">=0.2.0\", tier: pending, lane: 1}\n- {plugin: standalone:mysql, version: \">=0.2.0\", tier: pending, lane: 1}",
		"capability name": `- {plugin: mysql, version: ">=0.2.0", tier: certified, lane: 1, capabilities: {x: {test: "just a name"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "certification.yaml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, problems := LoadCertification(p); len(problems) == 0 {
				t.Fatal("expected a problem")
			}
		})
	}
}

func TestCaseReady(t *testing.T) {
	pg := ClassDebeziumPostgres
	mk := func(family string, args []string, classes ...string) (Case, []InputFile) {
		var keys []KeyOccurrence
		for _, cl := range classes {
			keys = append(keys, KeyOccurrence{Key: "connector.class", Value: cl})
		}
		return Case{Manifest: Manifest{Family: family, Command: Command{Args: args}}}, []InputFile{{Keys: keys}}
	}
	plain := []string{"migrate", "kafka-connect", "input/c.json"}
	worker := []string{"migrate", "kafka-connect", "--worker", "input/w.properties", "input/c.json"}
	none := RuleSet{Classes: map[string]bool{}}
	shared := RuleSet{Classes: map[string]bool{}, Shared: true}
	withPG := RuleSet{Classes: map[string]bool{pg: true}, Shared: true}
	all := RuleSet{Classes: map[string]bool{pg: true}, Shared: true, Worker: true}

	type readyCase struct {
		name  string
		c     Case
		in    []InputFile
		rs    RuleSet
		ready bool
	}
	var tests []readyCase
	add := func(name string, rs RuleSet, ready bool, c Case, in []InputFile) {
		tests = append(tests, readyCase{name, c, in, rs, ready})
	}
	c, in := mk("debezium-postgres", plain, pg)
	add("command landed, no rule files: pending", none, false, c, in)
	add("shared only: family still pending", shared, false, c, in)
	add("family file: required", withPG, true, c, in)
	c, in = mk("common", plain, "FileStreamSource")
	add("unsupported class without shared file: pending", none, false, c, in)
	add("unsupported class with shared file: required", shared, true, c, in)
	c, in = mk("common", worker, pg)
	add("worker case without worker file: pending", withPG, false, c, in)
	add("worker case with worker file: required", all, true, c, in)
	c, in = mk("kafka-mirror", plain)
	add("rejected input uses the family class", shared, false, c, in)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, why := CaseReady(tt.c, tt.in, tt.rs)
			if ready != tt.ready {
				t.Fatalf("ready = %v (%s), want %v", ready, why, tt.ready)
			}
			if !ready && why == "" {
				t.Fatal("a pending case must say what is missing")
			}
		})
	}
}

func TestLoadRuleSet(t *testing.T) {
	is := is.New(t)
	root := t.TempDir()
	rs, err := LoadRuleSet(root)
	is.NoErr(err)
	is.Equal(len(rs.Classes), 0) // no directory: nothing ready
	dir := filepath.Join(root, filepath.FromSlash(RuleDataDir))
	is.NoErr(os.MkdirAll(dir, 0o755))
	is.NoErr(os.WriteFile(filepath.Join(dir, "README.md"), []byte("# rules"), 0o600))
	is.NoErr(os.WriteFile(filepath.Join(dir, "shared.yaml"), []byte("rulesVersion: 1\nscope: shared\nkeys: []\n"), 0o600))
	is.NoErr(os.WriteFile(filepath.Join(dir, "debezium-postgres.yaml"), []byte("rulesVersion: 1\nscope: source\nclass: io.debezium.connector.postgresql.PostgresConnector\n"), 0o600))
	rs, err = LoadRuleSet(root)
	is.NoErr(err)
	is.True(rs.Shared)
	is.True(!rs.Worker)
	is.True(rs.Classes[ClassDebeziumPostgres])
}
