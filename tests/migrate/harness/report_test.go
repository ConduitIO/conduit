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
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

// The harness self-test (design rule 6): a correct output passes, and each
// way a migrator can be wrong makes CheckOutput fail. If any mutation below
// stops failing, the harness has lost the ability to catch that bug.

const selfTestInput = `{"name":"orders","config":{"connector.class":"io.debezium.connector.postgresql.PostgresConnector","database.hostname":"pg.internal.example","database.password":"PLACEHOLDER_SECRET_DB_PASSWORD","schema.include.list":"inventory"}}`

func selfTestCase() (Case, []InputFile) {
	two := 2
	yes, no := true, false
	m := Manifest{
		ID: "self", Family: "debezium-postgres",
		Command: Command{Args: []string{"migrate", "kafka-connect", "input/connector.json"}},
		Expect: Expect{
			ExitCode: &two,
			Notes:    []string{"kc.default_differs"},
			Entries: []EntryExpect{
				{Key: "schema.include.list", Status: StatusUnsupported, Blocking: &yes},
				{Key: "database.password", Status: StatusTranslated, Secret: &yes},
				{Key: "database.hostname", Status: StatusTranslated, Blocking: &no},
			},
		},
	}
	f, err := ParseInput("input/connector.json", []byte(selfTestInput))
	if err != nil {
		panic(err)
	}
	f.Label = "connector.json"
	return Case{Dir: "/corpus/debezium-postgres/self", Rel: "debezium-postgres/self", Manifest: m}, []InputFile{f}
}

func goodReport() (Summary, Report) {
	origin := func(p string) Origin { return Origin{File: "connector.json", Pointer: p} }
	rep := Report{
		ReportVersion: 1,
		Generator:     map[string]any{"conduit": "v0.21.0"},
		Connectors: []ReportConnector{{
			Name: "orders", Class: "io.debezium.connector.postgresql.PostgresConnector", Role: "source",
			Pipeline: "pipelines/orders.yaml", PipelineStatus: statusStopped,
			Entries: []Entry{
				{Key: "name", Status: StatusTranslated, Origin: origin("/name")},
				{Key: "connector.class", Status: StatusTranslated, Origin: origin("/config/connector.class")},
				{Key: "database.hostname", Status: StatusTranslated, Origin: origin("/config/database.hostname"), Target: &Target{ConfigPath: "/pipelines/0/connectors/0/settings/url"}},
				{Key: "database.password", Status: StatusTranslated, Secret: true, Origin: origin("/config/database.password"), Target: &Target{ConfigPath: "/pipelines/0/connectors/0/settings/url", Env: "KC_ORDERS_DATABASE_PASSWORD"}},
				{Key: "schema.include.list", Status: StatusUnsupported, Blocking: true, Code: "kc.key_unsupported", Reason: "no literal table list", Fix: "list the tables", Origin: origin("/config/schema.include.list")},
			},
			Notes: []Note{{Code: "kc.default_differs", Reason: "plugin.name defaults to decoderbufs", Fix: "none needed"}},
		}},
	}
	return Summary{Connectors: 1, Keys: 5, Translated: 4, Unsupported: 1, Blocking: 1}, rep
}

const goodPipeline = `version: "2.2"
pipelines:
  - id: orders
    status: stopped
    description: 'Migrated from Kafka Connect connector "orders" (io.debezium.connector.postgresql.PostgresConnector) by conduit v0.21.0; report sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
    connectors:
      - id: source
        plugin: builtin:postgres
      - id: destination
        plugin: builtin:kafka
`

func buildOutput(t *testing.T, ok bool, sum Summary, rep Report, exit int) Output {
	t.Helper()
	result, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(map[string]any{"command": commandMigrate, "ok": ok, "summary": sum, "result": json.RawMessage(result), "error": nil})
	if err != nil {
		t.Fatal(err)
	}
	return Output{ExitCode: exit, Stdout: env, Files: map[string][]byte{
		"report.json":           result,
		"pipelines/orders.yaml": []byte(goodPipeline),
		"REPORT.md":             []byte("# Report\n"),
	}}
}

func TestCheckOutput_GoodOutputPasses(t *testing.T) {
	c, inputs := selfTestCase()
	sum, rep := goodReport()
	res := CheckOutput(Check{Case: c, Inputs: inputs, Out: buildOutput(t, false, sum, rep, 2), HostPaths: []string{"/tmp/run-a"}})
	if len(res.Problems) != 0 {
		t.Fatalf("a correct output failed the harness:\n%s", strings.Join(res.Problems, "\n"))
	}
	is.New(t).Equal(res.Plugins, []string{"builtin:postgres", "builtin:kafka"})
}

func TestCheckOutput_Mutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, sum *Summary, rep *Report, out *Output, ok *bool, exit *int)
		want   string
	}{{
		name: "a key is silently dropped",
		mutate: func(_ *testing.T, sum *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			e := rep.Connectors[0].Entries
			rep.Connectors[0].Entries = slices.Delete(slices.Clone(e), 2, 3)
			sum.Keys, sum.Translated = 4, 3
		},
		want: "silently dropped",
	}, {
		name: "entries out of input order",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			e := rep.Connectors[0].Entries
			e[2], e[3] = e[3], e[2]
		},
		want: "input order",
	}, {
		// The deliberately wrong rule of rule 6: one key flipped from
		// unsupported to translated, with a self-consistent report.
		name: "a blocking key flipped to translated",
		mutate: func(_ *testing.T, sum *Summary, rep *Report, out *Output, ok *bool, exit *int) {
			e := &rep.Connectors[0].Entries[4]
			e.Status, e.Blocking, e.Code, e.Reason, e.Fix = StatusTranslated, false, "", "", ""
			*sum = Summary{Connectors: 1, Keys: 5, Translated: 5}
			rep.Connectors[0].PipelineStatus = statusRunning
			out.Files["pipelines/orders.yaml"] = []byte(strings.Replace(goodPipeline, "status: stopped", "status: running", 1))
			*ok, *exit = true, 0
		},
		want: "status translated, want unsupported",
	}, {
		name: "a secret canary reaches REPORT.md",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, out *Output, _ *bool, _ *int) {
			out.Files["REPORT.md"] = []byte("password is PLACEHOLDER_SECRET_DB_PASSWORD\n")
		},
		want: "canary",
	}, {
		name:   "summary does not match the entries",
		mutate: func(_ *testing.T, sum *Summary, _ *Report, _ *Output, _ *bool, _ *int) { sum.Keys = 6 },
		want:   "does not match the entries",
	}, {
		name: "blocking connector written running",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].PipelineStatus = statusRunning
		},
		want: "pipelineStatus",
	}, {
		name: "an unexpected kc.key_unknown",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].Entries[4].Code = codeKeyUnknown
		},
		want: "expectUnknown",
	}, {
		name: "a host path in the output",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, out *Output, _ *bool, _ *int) {
			out.Files["REPORT.md"] = []byte("written to /tmp/run-a/out\n")
		},
		want: "host path",
	}, {
		name: "a hard failure on a corpus input",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, out *Output, _ *bool, _ *int) {
			out.Stdout = []byte(`{"command":"migrate.kafka-connect","ok":false,"summary":null,"result":null,"error":{"code":"internal.error","message":"boom"}}`)
		},
		want: "hard failure",
	}, {
		name: "a credential not marked secret",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].Entries[3].Secret = false
		},
		want: "not marked secret",
	}, {
		name: "origin points at the wrong place",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].Entries[2].Origin.Pointer = "/config/database.port"
		},
		want: "origin",
	}, {
		name: "an entry for a key that is not in the input",
		mutate: func(_ *testing.T, sum *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].Entries = append(rep.Connectors[0].Entries, Entry{Key: "made.up", Status: StatusTranslated, Origin: Origin{File: "connector.json", Pointer: "/config/made.up"}})
			sum.Keys, sum.Translated = 6, 5
		},
		want: "not in the input",
	}, {
		name:   "exit 0 with a blocking entry",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, _ *Output, _ *bool, exit *int) { *exit = 0 },
		want:   "exit code 0",
	}, {
		name: "the pipeline file is missing",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, out *Output, _ *bool, _ *int) {
			delete(out.Files, "pipelines/orders.yaml")
		},
		want: "was not written",
	}, {
		name: "report.json disagrees with the --json result",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, out *Output, _ *bool, _ *int) {
			out.Files["report.json"] = []byte(`{"reportVersion":1}`)
		},
		want: "differs from the --json result",
	}, {
		name: "an unsupported entry without a fix",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			rep.Connectors[0].Entries[4].Fix = ""
		},
		want: "need code and fix",
	}, {
		name: "absolute origin file",
		mutate: func(_ *testing.T, _ *Summary, rep *Report, _ *Output, _ *bool, _ *int) {
			for i := range rep.Connectors[0].Entries {
				rep.Connectors[0].Entries[i].Origin.File = "/home/me/connector.json"
			}
		},
		want: "must be relative",
	}, {
		name:   "ok true with a blocking entry",
		mutate: func(_ *testing.T, _ *Summary, _ *Report, _ *Output, ok *bool, _ *int) { *ok = true },
		want:   "ok is true",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, inputs := selfTestCase()
			sum, rep := goodReport()
			ok, exit := false, 2
			out := buildOutput(t, ok, sum, rep, exit) // files the mutation may edit
			origReport := string(out.Files["report.json"])
			tt.mutate(t, &sum, &rep, &out, &ok, &exit)
			if !strings.Contains(string(out.Stdout), "internal.error") {
				rebuilt := buildOutput(t, ok, sum, rep, exit)
				out.Stdout, out.ExitCode = rebuilt.Stdout, rebuilt.ExitCode
				if string(out.Files["report.json"]) == origReport { // keep a deliberate edit
					out.Files["report.json"] = rebuilt.Files["report.json"]
				}
			}
			res := CheckOutput(Check{Case: c, Inputs: inputs, Out: out, HostPaths: []string{"/tmp/run-a"}})
			joined := strings.Join(res.Problems, "\n")
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("mutation not caught: want a problem containing %q, got:\n%s", tt.want, joined)
			}
		})
	}
}

func TestCheckOutput_RejectedInput(t *testing.T) {
	is := is.New(t)
	c, inputs := selfTestCase()
	two := 2
	c.Manifest.Expect = Expect{ExitCode: &two, Error: "migrate.input_unrecognized"}
	rejected := Output{ExitCode: 2, Stdout: []byte(`{"command":"migrate.kafka-connect","ok":false,"summary":null,"result":null,"error":{"code":"migrate.input_unrecognized","message":"no connector"}}`)}
	is.Equal(len(CheckOutput(Check{Case: c, Inputs: inputs, Out: rejected}).Problems), 0)

	// Translating part of a rejected input is caught.
	sum, rep := goodReport()
	res := CheckOutput(Check{Case: c, Inputs: inputs, Out: buildOutput(t, false, sum, rep, 2)})
	is.True(strings.Contains(strings.Join(res.Problems, "\n"), "must be rejected"))
}

func TestCompareRuns(t *testing.T) {
	is := is.New(t)
	a := Output{Stdout: []byte("{}\n"), Files: map[string][]byte{"x": []byte("1")}}
	b := Output{Stdout: []byte("{}\n"), Files: map[string][]byte{"x": []byte("1")}}
	is.Equal(len(CompareRuns(a, b)), 0)
	b.Files["x"] = []byte("2")
	is.Equal(len(CompareRuns(a, b)), 1)
	b.Files = map[string][]byte{}
	is.Equal(len(CompareRuns(a, b)), 1)
}

func TestNormalize(t *testing.T) {
	is := is.New(t)
	rep := Normalize("report.json", []byte(`{"generator":{"conduit":"v0.21.0-nightly.20261014","rules":{"debezium-postgres":1}}}`))
	is.Equal(string(rep), `{"generator":{"conduit":"<VERSION>","rules":{"debezium-postgres":1}}}`)
	pl := Normalize("pipelines/orders.yaml", []byte(goodPipeline))
	is.True(strings.Contains(string(pl), "by conduit <VERSION>; report sha256:<REPORT_SHA256>"))
	// Nothing else is normalized: a rules version change stays visible.
	is.True(strings.Contains(string(rep), `"debezium-postgres":1`))
}

func TestCompareGoldens(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	out := Output{ExitCode: 2, Files: map[string][]byte{"report.json": []byte(`{"generator":{"conduit":"v1"}}`), "pipelines/a.yaml": []byte(goodPipeline)}}
	p, err := CompareGoldens(dir, out, false)
	is.NoErr(err)
	is.True(strings.Contains(strings.Join(p, ""), "-update"))
	_, err = CompareGoldens(dir, out, true)
	is.NoErr(err)
	p, err = CompareGoldens(dir, out, false)
	is.NoErr(err)
	is.Equal(len(p), 0)
	// Another conduit version still matches; another exit code does not.
	out.Files["report.json"] = []byte(`{"generator":{"conduit":"v2"}}`)
	out.ExitCode = 0
	p, err = CompareGoldens(dir, out, false)
	is.NoErr(err)
	is.Equal(p, []string{"output exit_code differs from its golden at line 1 (re-run with -update and review)"})
}

func TestSplitMCPFiles(t *testing.T) {
	is := is.New(t)
	for _, sc := range []string{
		`{"ok":true,"result":{"reportVersion":1},"files":[{"path":"pipelines/a.yaml","content":"x"}]}`,
		`{"ok":true,"result":{"reportVersion":1,"files":[{"path":"pipelines/a.yaml","content":"x"}]}}`,
	} {
		out, err := splitMCPFiles([]byte(sc))
		is.NoErr(err)
		is.Equal(string(out.Files["pipelines/a.yaml"]), "x")
		is.True(!strings.Contains(string(out.Stdout), "files"))
	}
}
