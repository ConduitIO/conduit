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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/spf13/cobra"
)

const connectorJSON = `{"name": "orders", "config": {"connector.class": "com.example.Unknown", "a": "1"}}`

type run struct {
	cmd    *cobra.Command
	stdout string
	err    error
}

func execute(t *testing.T, stdin string, args ...string) run {
	t.Helper()
	c := &KafkaConnectCommand{stdin: strings.NewReader(stdin)}
	cmd := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{})).MustBuildCobraCommand(c)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	executed, err := cmd.ExecuteC()
	return run{cmd: executed, stdout: out.String(), err: err}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKafkaConnect_JSONEnvelope(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "in/orders.json", connectorJSON)
	r := execute(t, "", p, "--json", "--dry-run")
	is.NoErr(r.err)

	is.NoErr(testutils.ValidateEnvelope([]byte(r.stdout)))
	var env struct {
		Command string          `json:"command"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
		Error   any             `json:"error"`
	}
	is.NoErr(json.Unmarshal([]byte(r.stdout), &env))
	is.Equal(env.Command, "migrate.kafka-connect")
	is.True(!env.OK)          // an unknown class blocks
	is.True(env.Error == nil) // findings are not a hard error
	is.NoErr(schema.ValidateReport(env.Result))
	is.True(strings.Contains(string(env.Result), `"file": "orders.json"`)) // label relative to the input's directory

	code, ok := cecdysis.ResultExitCode(r.cmd)
	is.True(ok)
	is.Equal(code, 2)
	_, err := os.Stat("conduit-migration")
	is.True(os.IsNotExist(err)) // --dry-run wrote nothing
}

func TestKafkaConnect_WritesOutputs(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "orders.json", connectorJSON)
	out := filepath.Join(dir, "out")

	r := execute(t, "", p, "--out", out, "--accept-gaps")
	is.NoErr(r.err)
	_, recorded := cecdysis.ResultExitCode(r.cmd)
	is.True(!recorded) // accepted gaps exit 0
	for _, f := range []string{"report.json", "REPORT.md"} {
		_, err := os.Stat(filepath.Join(out, f))
		is.NoErr(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "report.json"))
	is.NoErr(err)
	is.NoErr(schema.ValidateReport(b))
	is.True(strings.Contains(r.stdout, "Wrote report.json and REPORT.md to "+out))

	// A second run refuses the non-empty directory, then --force overwrites.
	r = execute(t, "", p, "--out", out, "--json")
	is.True(r.err != nil)
	is.Equal(errCode(t, r.stdout), "migrate.output_exists")
	r = execute(t, "", p, "--out", out, "--force")
	is.NoErr(r.err)
}

func TestKafkaConnect_Stdin(t *testing.T) {
	is := is.New(t)
	r := execute(t, connectorJSON, "-", "--json", "--dry-run")
	is.NoErr(r.err)
	var env struct {
		Result kcmigrate.Report `json:"result"`
	}
	is.NoErr(json.Unmarshal([]byte(r.stdout), &env))
	is.Equal(env.Result.Connectors[0].Entries[0].Origin.File, "<stdin>")
}

func TestKafkaConnect_HumanOutput(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "orders.json", connectorJSON)
	r := execute(t, "", p, "--dry-run", "--no-color")
	is.NoErr(r.err)
	is.True(strings.Contains(r.stdout, "orders  com.example.Unknown  ->  no pipeline"))
	is.True(strings.Contains(r.stdout, "kc.connector_class_unsupported"))
	is.True(strings.Contains(r.stdout, "orders.json#/config/a:"))
	is.True(strings.Contains(r.stdout, "Summary: 2 keys · 0 translated · 0 approximated · 2 unsupported · 2 blocking"))
	is.True(strings.Contains(r.stdout, "Fix the ✗ items above, then re-run."))
	is.True(strings.Contains(r.stdout, "Dry run: no files written."))
}

func TestKafkaConnect_InputErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mixed/a.json", connectorJSON)
	writeFile(t, dir, "mixed/notes.txt", "x")
	tests := []struct {
		name string
		args []string
		code string
	}{
		{"missing file", []string{filepath.Join(dir, "nope.json")}, "migrate.input_unreadable"},
		{"directory with other files", []string{filepath.Join(dir, "mixed")}, "migrate.input_unrecognized"},
		{"stdin twice", []string{"-", "-"}, "migrate.input_unreadable"},
		{"bad format", []string{"-", "--format", "xml"}, "migrate.input_unrecognized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			r := execute(t, connectorJSON, append(tt.args, "--json", "--dry-run")...)
			is.True(r.err != nil)
			is.NoErr(testutils.ValidateEnvelope([]byte(r.stdout)))
			is.Equal(errCode(t, r.stdout), tt.code)
		})
	}
}

func TestReadInputs_DirectoryAndLabels(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	writeFile(t, dir, "conn/b.properties", "name=b\n")
	writeFile(t, dir, "conn/a.json", connectorJSON)
	writeFile(t, dir, "conn/.hidden", "x")
	w := writeFile(t, dir, "worker/connect.properties", "group.id=g\n")

	inputs, worker, err := readInputs([]string{filepath.Join(dir, "conn")}, w, strings.NewReader(""))
	is.NoErr(err)
	is.Equal(len(inputs), 2)
	is.Equal(inputs[0].Name, "conn/a.json") // sorted, relative to the common parent
	is.Equal(inputs[1].Name, "conn/b.properties")
	is.Equal(worker.Name, "worker/connect.properties")
}

func TestCommonDir(t *testing.T) {
	is := is.New(t)
	is.Equal(commonDir(nil), "")
	is.Equal(commonDir([]string{"/a/b/c.json"}), "/a/b")
	is.Equal(commonDir([]string{"/a/b/c.json", "/a/b/d/e.json"}), "/a/b")
	is.Equal(commonDir([]string{"/a/bc/x", "/a/b/y"}), "/a")
	is.Equal(commonDir([]string{"/x/1", "/y/2"}), "/")
}

func errCode(t *testing.T, stdout string) string {
	t.Helper()
	var env cecdysis.Result
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, stdout)
	}
	if env.Error == nil {
		return ""
	}
	return env.Error.Code
}
