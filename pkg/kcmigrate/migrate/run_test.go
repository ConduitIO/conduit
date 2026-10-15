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

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
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
	cleanConnector    = "name=clean\nconnector.class=com.example.ExampleSource\nexample.host=db.internal.example\nexample.mode=fast\n"
	approxConnector   = "name=approx\nconnector.class=com.example.ExampleSource\nexample.mode=slow\n"
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
			res, err := Run(context.Background(), testEngine(t), Request{Inputs: tt.inputs, Strict: tt.strict, AcceptGaps: tt.acc, ConduitVersion: "v0.21.0-test"})
			is.NoErr(err)
			is.Equal(res.OK, tt.wantOK)
			is.Equal(len(res.Report.AcceptedGaps), tt.wantGaps)
			is.Equal(len(res.Files), 2)
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
	res, err := Run(context.Background(), testEngine(t), Request{
		Inputs: []kcmigrate.Input{
			in("connectors/clean.properties", cleanConnector+"example.mode=turbo\n"),
			in("connectors/blocking.json", blockingConnector),
		},
		Worker:         &worker,
		ConduitVersion: "v0.21.0-test",
	})
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
	res, err := Run(context.Background(), testEngine(t), Request{
		Inputs: []kcmigrate.Input{in("b.json", blockingConnector), in("c.properties", "name=c\nconnector.class=com.example.ExampleSource\nexample.password=CANARY-pw-2\nexample.mode=CANARY-mode\n")},
	})
	is.NoErr(err)
	for _, f := range res.Files {
		is.True(!bytes.Contains(f.Content, []byte("CANARY")))
	}
}

func TestRun_Deterministic(t *testing.T) {
	is := is.New(t)
	req := Request{Inputs: []kcmigrate.Input{in("b.json", blockingConnector), in("a.properties", cleanConnector)}, ConduitVersion: "v"}
	first, err := Run(context.Background(), testEngine(t), req)
	is.NoErr(err)
	for range 10 {
		again, err := Run(context.Background(), testEngine(t), req)
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
			_, err := Run(context.Background(), testEngine(t), Request{Inputs: tt.inputs})
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
	_, err := Run(ctx, testEngine(t), Request{Inputs: []kcmigrate.Input{in("a.properties", cleanConnector)}})
	is.True(err != nil && strings.Contains(err.Error(), "canceled"))
}
