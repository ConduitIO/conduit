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
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	provyaml "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"github.com/matryer/is"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testEngine(t *testing.T) *rules.Engine {
	t.Helper()
	e, err := rules.Load(os.DirFS("../rules/testdata/valid"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func in(name, content string) kcmigrate.Input {
	return kcmigrate.Input{Name: name, Content: []byte(content)}
}

const (
	cleanConnector    = "name=clean\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.host=db.internal.example\nexample.mode=fast\n"
	approxConnector   = "name=approx\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.mode=slow\n"
	blockingConnector = `{"name": "blocking", "config": {"connector.class": "com.example.Unknown", "secret.password": "CANARY-pw-1"}}`
)

func TestRun_Verdicts(t *testing.T) {
	tests := []struct {
		name        string
		inputs      []kcmigrate.Input
		strict, acc bool
		wantOK      bool
		wantGaps    int
	}{
		{"clean", []kcmigrate.Input{in("a.properties", cleanConnector)}, false, false, true, 0},
		{"approximated passes", []kcmigrate.Input{in("a.properties", approxConnector)}, false, false, true, 0},
		{"approximated fails under strict", []kcmigrate.Input{in("a.properties", approxConnector)}, true, false, false, 0},
		{"blocking fails", []kcmigrate.Input{in("b.json", blockingConnector)}, false, false, false, 0},
		{"blocking accepted", []kcmigrate.Input{in("b.json", blockingConnector)}, false, true, true, 2},
		{"accepted gaps fail under strict", []kcmigrate.Input{in("b.json", blockingConnector)}, true, true, false, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			res, err := Run(context.Background(), testEngine(t), withValidator(Request{Inputs: tt.inputs, Strict: tt.strict, AcceptGaps: tt.acc, ConduitVersion: "v0.21.0-test"}))
			is.NoErr(err)
			is.Equal(res.OK, tt.wantOK)
			is.Equal(len(res.Report.AcceptedGaps), tt.wantGaps)
			is.True(len(res.Files) >= 2)
			is.Equal(res.Files[0].Path, ReportFile)
			is.NoErr(schema.ValidateReport(res.Files[0].Content))
		})
	}
}

// TestRun_Golden pins report.json and REPORT.md for a run with a worker, a
// clean connector, a duplicate key and an unknown class.
func TestRun_Golden(t *testing.T) {
	is := is.New(t)
	worker := in("connect-distributed.properties", "group.id=connect\nbootstrap.servers=k.internal.example:9092\n")
	res, err := Run(context.Background(), testEngine(t), withValidator(Request{
		Inputs: []kcmigrate.Input{
			in("connectors/clean.properties", cleanConnector+"example.mode=turbo\n"),
			in("connectors/blocking.json", blockingConnector),
		},
		Worker:         &worker,
		ConduitVersion: "v0.21.0-test",
	}))
	is.NoErr(err)
	is.True(!res.OK)
	for _, f := range res.Files {
		golden := filepath.Join("testdata", "golden", f.Path)
		if *update {
			is.NoErr(os.MkdirAll(filepath.Dir(golden), 0o755))
			is.NoErr(os.WriteFile(golden, f.Content, 0o600))
		}
		want, err := os.ReadFile(golden)
		is.NoErr(err)
		if !bytes.Equal(f.Content, want) {
			t.Errorf("%s is stale; run go test ./pkg/kcmigrate/migrate -run TestRun_Golden -update and review the diff", golden)
		}
	}
	is.Equal(res.Report.Connectors[0].Role, kcmigrate.RoleWorker)
	is.Equal(res.Report.Summary.Connectors, 3)
}

func TestRun_NoValueReachesOutput(t *testing.T) {
	is := is.New(t)
	res, err := Run(context.Background(), testEngine(t), withValidator(Request{
		Inputs: []kcmigrate.Input{in("b.json", blockingConnector), in("c.properties", "name=c\nconnector.class=com.example.ExampleSource\nexample.password=CANARY-pw-2\nexample.mode=CANARY-mode\n")},
	}))
	is.NoErr(err)
	for _, f := range res.Files {
		is.True(!bytes.Contains(f.Content, []byte("CANARY")))
	}
}

func TestRun_Deterministic(t *testing.T) {
	is := is.New(t)
	req := Request{Inputs: []kcmigrate.Input{in("b.json", blockingConnector), in("a.properties", cleanConnector)}, ConduitVersion: "v"}
	first, err := Run(context.Background(), testEngine(t), withValidator(req))
	is.NoErr(err)
	for range 10 {
		again, err := Run(context.Background(), testEngine(t), withValidator(req))
		is.NoErr(err)
		for i := range first.Files {
			is.Equal(first.Files[i].Content, again.Files[i].Content)
		}
	}
	is.Equal(first.Report.Connectors[0].Name, "blocking") // input order, not sorted
}

func TestRun_Errors(t *testing.T) {
	tests := []struct {
		name   string
		inputs []kcmigrate.Input
		code   conduiterr.Code
	}{
		{"no inputs", nil, kcmigrate.CodeInputUnrecognized},
		{"unnamed connector", []kcmigrate.Input{in("a.properties", "connector.class=X\n")}, CodeConnectorNameInvalid},
		{"duplicate names", []kcmigrate.Input{in("a.properties", cleanConnector), in("b.properties", cleanConnector)}, CodeConnectorNameInvalid},
		{"unreadable", []kcmigrate.Input{in("a.json", `{"name": `)}, kcmigrate.CodeInputUnreadable},
		{"too many", make([]kcmigrate.Input, kcmigrate.MaxInputs+1), kcmigrate.CodeInputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			_, err := Run(context.Background(), testEngine(t), withValidator(Request{Inputs: tt.inputs}))
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, tt.code)
			is.True(ce.Suggestion != "")
		})
	}
}

func TestRun_Canceled(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, testEngine(t), withValidator(Request{Inputs: []kcmigrate.Input{in("a.properties", cleanConnector)}}))
	is.True(err != nil && strings.Contains(err.Error(), "canceled"))
}

func runOne(t *testing.T, req Request) Result {
	t.Helper()
	if req.ConduitVersion == "" {
		req.ConduitVersion = "v"
	}
	res, err := Run(context.Background(), testEngine(t), withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func file(res Result, path string) string {
	for _, f := range res.Files {
		if f.Path == path {
			return string(f.Content)
		}
	}
	return ""
}

// Every generated pipeline parses with the engine's own pipeline parser and
// passes its config validation.
func TestRun_PipelinesParseAndValidate(t *testing.T) {
	is := is.New(t)
	res := runOne(t, Request{Inputs: []kcmigrate.Input{in("a.properties", cleanConnector), in("b.properties", approxConnector)}})
	parser := provyaml.NewParser(log.Nop())
	n := 0
	for _, f := range res.Files {
		if !strings.HasPrefix(f.Path, "pipelines/") {
			continue
		}
		n++
		ps, err := parser.Parse(context.Background(), bytes.NewReader(f.Content))
		is.NoErr(err)
		is.Equal(len(ps), 1)
		is.NoErr(config.Validate(ps[0]))
	}
	is.Equal(n, 2)
	is.Equal(res.Report.Connectors[0].Pipeline, "pipelines/clean.yaml")
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineRunning)
	is.Equal(entryFor(res.Report.Connectors[0], "example.host").Target.ConfigPath, "/pipelines/0/connectors/0/settings/host")
}

func TestRun_Secrets(t *testing.T) {
	is := is.New(t)
	res := runOne(t, Request{Inputs: []kcmigrate.Input{
		in("lit.properties", "name=lit\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.password=CANARY-pw\n"),
		in("env.properties", "name=env\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.password=${env:PG_PASSWORD}\n"),
		in("file.properties", "name=file\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.password=${file:/etc/kc/db.properties:password}\n"),
	}})
	for _, f := range res.Files {
		is.True(!strings.Contains(string(f.Content), "CANARY"))
	}
	is.True(strings.Contains(file(res, "pipelines/lit.yaml"), "password: ${KC_LIT_EXAMPLE_PASSWORD}"))
	is.True(strings.Contains(file(res, "pipelines/env.yaml"), "password: ${PG_PASSWORD}"))
	is.True(strings.Contains(file(res, "pipelines/file.yaml"), "password: ${KC_FILE_EXAMPLE_PASSWORD}"))

	tmpl := file(res, SecretsTemplateFile)
	is.True(strings.Contains(tmpl, "\nKC_LIT_EXAMPLE_PASSWORD=\n"))
	is.True(strings.Contains(tmpl, "\nPG_PASSWORD=\n"))
	is.True(strings.Contains(tmpl, "from ${file:/etc/kc/db.properties:password}"))

	lit, env, fileC := res.Report.Connectors[0], res.Report.Connectors[1], res.Report.Connectors[2]
	pw := entryFor(lit, "example.password")
	is.True(pw.Secret)
	is.Equal(pw.Target.Env, "KC_LIT_EXAMPLE_PASSWORD")
	is.Equal(len(lit.Notes), 1)
	is.Equal(lit.Notes[0].Code, "kc.secret_plaintext")
	is.Equal(len(env.Notes), 0) // references are not plaintext
	is.Equal(len(fileC.Notes), 0)
	is.True(strings.Contains(entryFor(fileC, "example.password").Reason, "${file:/etc/kc/db.properties:password}"))
}

func TestRun_ConflictingKeysBlock(t *testing.T) {
	is := is.New(t)
	res := runOne(t, Request{Inputs: []kcmigrate.Input{in("a.properties",
		"name=a\nconnector.class=com.example.ExampleSource\nexample.host=h1\nexample.old.host=h2\n")}})
	e := entryFor(res.Report.Connectors[0], "example.old.host")
	is.Equal(e.Code, "kc.key_conflict")
	is.True(e.Blocking)
	is.True(strings.Contains(file(res, "pipelines/a.yaml"), "host: h1")) // the first owner stays, the pipeline is stopped
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineStopped)
}

func TestRun_WorkerDefaults(t *testing.T) {
	is := is.New(t)
	worker := in("w.properties", "bootstrap.servers=worker:9092\n")
	res := runOne(t, Request{Worker: &worker, Inputs: []kcmigrate.Input{
		in("a.properties", cleanConnector),
		in("b.properties", "name=b\nconnector.class=com.example.ExampleSource\nexample.servers=own:9092\n"),
	}})
	is.True(strings.Contains(file(res, "pipelines/clean.yaml"), "servers: worker:9092")) // inherited
	is.True(strings.Contains(file(res, "pipelines/b.yaml"), "servers: own:9092"))        // the connector wins
	is.True(res.Report.Connectors[0].Entries[0].Target == nil)                           // worker entries have no single target
}

func TestRun_PipelineStatus(t *testing.T) {
	is := is.New(t)
	paused := `{"orders": {"info": {"name": "orders", "config": {"connector.class": "com.example.ExampleSource", "name": "orders", "key.converter": "b", "value.converter": "b"}}, "status": {"connector": {"state": "PAUSED"}}}}`
	res := runOne(t, Request{Inputs: []kcmigrate.Input{in("e.json", paused)}})
	is.True(res.OK)
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineStopped) // paused in Kafka Connect

	blocked := "name=x\nconnector.class=com.example.ExampleSource\nkey.converter=b\nvalue.converter=b\nexample.mode=turbo\n"
	res = runOne(t, Request{Inputs: []kcmigrate.Input{in("x.properties", blocked)}, AcceptGaps: true})
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineRunning) // --accept-gaps
	is.True(strings.Contains(file(res, "pipelines/x.yaml"), "status: running"))
}

func TestRun_ValidatorFailureIsInternal(t *testing.T) {
	is := is.New(t)
	_, err := Run(context.Background(), testEngine(t), withValidator(Request{
		Inputs:           []kcmigrate.Input{in("a.properties", cleanConnector)},
		ValidatePipeline: func(string, []byte) error { return cerrors.New("bad") },
	}))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInternal)
}

func TestRun_PipelineFileCollision(t *testing.T) {
	is := is.New(t)
	_, err := Run(context.Background(), testEngine(t), withValidator(Request{Inputs: []kcmigrate.Input{
		in("a.properties", "name=a/b\nconnector.class=com.example.ExampleSource\n"),
		in("b.properties", "name=a_b\nconnector.class=com.example.ExampleSource\n"),
	}}))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorNameInvalid)
}

// Secret variables are unique across the run: two connectors whose names
// sanitize alike (my-conn, my_conn) must not share a variable, or one
// connector would run with the other's password.
func TestRun_SecretEnvUniqueAcrossConnectors(t *testing.T) {
	is := is.New(t)
	res := runOne(t, Request{Inputs: []kcmigrate.Input{
		in("a.properties", "name=my-conn\nconnector.class=com.example.ExampleSource\nexample.password=A\n"),
		in("b.properties", "name=my_conn\nconnector.class=com.example.ExampleSource\nexample.password=B\n"),
	}})
	a := entryFor(res.Report.Connectors[0], "example.password").Target.Env
	b := entryFor(res.Report.Connectors[1], "example.password").Target.Env
	is.True(a != b)
	tmpl := file(res, SecretsTemplateFile)
	is.True(strings.Contains(tmpl, "\n"+a+"=\n"))
	is.True(strings.Contains(tmpl, "\n"+b+"=\n"))
	is.True(strings.Contains(file(res, "pipelines/my-conn.yaml"), "${"+a+"}"))
	is.True(strings.Contains(file(res, "pipelines/my_conn.yaml"), "${"+b+"}"))
}

// A blocking worker entry applies to every pipeline that inherits the
// worker, so they are written stopped.
func TestRun_BlockingWorkerEntryStopsPipelines(t *testing.T) {
	is := is.New(t)
	worker := in("w.properties", "worker.blocked=x\n")
	res := runOne(t, Request{Worker: &worker, Inputs: []kcmigrate.Input{in("a.properties", cleanConnector)}})
	is.True(!res.OK)
	is.Equal(res.Report.Connectors[1].PipelineStatus, kcmigrate.PipelineStopped)
	is.True(strings.Contains(file(res, "pipelines/clean.yaml"), "status: stopped"))

	res = runOne(t, Request{Worker: &worker, AcceptGaps: true, Inputs: []kcmigrate.Input{in("a.properties", cleanConnector)}})
	is.Equal(res.Report.Connectors[1].PipelineStatus, kcmigrate.PipelineRunning)
}

func TestRun_RequiresAValidator(t *testing.T) {
	is := is.New(t)
	_, err := Run(context.Background(), testEngine(t), Request{Inputs: []kcmigrate.Input{in("a.properties", cleanConnector)}, ConduitVersion: "v"})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInternal)
}

func TestRun_ConverterUnknownNotes(t *testing.T) {
	is := is.New(t)
	bare := "name=bare\nconnector.class=com.example.ExampleSource\n"
	res := runOne(t, Request{Inputs: []kcmigrate.Input{in("a.properties", bare)}})
	notes := res.Report.Connectors[0].Notes
	is.Equal(len(notes), 2)
	is.Equal(notes[0].Code, "kc.converter_unknown")
	is.True(notes[0].Blocking)
	is.Equal(notes[0].Rule, "shared/converters/key.converter")
	is.True(!res.OK)
	is.Equal(res.Report.Connectors[0].PipelineStatus, kcmigrate.PipelineStopped)

	// A worker file that sets value.converter leaves only the key note.
	worker := in("w.properties", "value.converter=b\n")
	res = runOne(t, Request{Worker: &worker, Inputs: []kcmigrate.Input{in("a.properties", bare)}})
	is.Equal(len(res.Report.Connectors[1].Notes), 1)
	is.Equal(res.Report.Connectors[1].Notes[0].Rule, "shared/converters/key.converter")
}

func entryFor(rc kcmigrate.ReportConnector, key string) kcmigrate.Entry {
	for _, e := range rc.Entries {
		if e.Key == key {
			return e
		}
	}
	return kcmigrate.Entry{}
}

// withValidator gives a request the engine's own pipeline parser and config
// validation as its required validator. The CLI passes the full
// `pipelines validate` engine, which this package cannot import.
func withValidator(req Request) Request {
	if req.ValidatePipeline == nil {
		req.ValidatePipeline = func(_ string, content []byte) error {
			ps, err := provyaml.NewParser(log.Nop()).Parse(context.Background(), bytes.NewReader(content))
			if err != nil {
				return err
			}
			for _, p := range ps {
				if err := config.Validate(p); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return req
}
