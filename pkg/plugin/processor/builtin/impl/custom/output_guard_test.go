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

package custom

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestCheckOutput(t *testing.T) {
	self := map[string]any{}
	self["self"] = self

	a := map[string]any{}
	b := []any{a}
	a["list"] = b // map -> slice -> map

	shared := map[string]any{"x": 1}

	deep := func(n int) any {
		root := map[string]any{}
		cur := root
		for i := 0; i < n-1; i++ {
			next := map[string]any{}
			cur["n"] = next
			cur = next
		}
		cur["leaf"] = 1
		return root
	}

	// x = [x, x] repeated: tiny depth, 2^n expanded size.
	var blowup any = []any{1}
	for i := 0; i < 40; i++ {
		blowup = []any{blowup, blowup}
	}

	typed := map[string][]string{"k": {"a", "b"}}
	selfSlice := make([]any, 1)
	selfSlice[0] = selfSlice

	tests := []struct {
		name     string
		in       any
		wantCode conduiterr.Code
		wantPath string
	}{
		{name: "nil", in: nil},
		{name: "scalar", in: "x"},
		{name: "raw data pointer", in: func() any { r := opencdc.RawData("x"); return &r }()},
		{name: "flat map", in: map[string]any{"a": 1, "b": []any{1, 2}}},
		{name: "typed containers", in: typed},
		{name: "empty containers", in: map[string]any{"a": map[string]any{}, "b": []any{}}},
		{name: "dag, same map twice", in: map[string]any{"p": shared, "q": shared}},
		{name: "dag, same map in slice", in: []any{shared, shared, shared}},
		{name: "depth at limit", in: deep(maxOutputDepth)},
		{name: "self map", in: self, wantCode: CodeJSCyclicOutput, wantPath: ".self"},
		{name: "map slice map cycle", in: a, wantCode: CodeJSCyclicOutput, wantPath: ".list[0]"},
		{name: "self slice", in: selfSlice, wantCode: CodeJSCyclicOutput, wantPath: "[0]"},
		{name: "too deep", in: deep(maxOutputDepth + 2), wantCode: CodeJSOutputLimitExceeded},
		{name: "exponential dag", in: blowup, wantCode: CodeJSOutputLimitExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := checkOutput("", tc.in)
			if tc.wantCode.IsZero() {
				is.True(err == nil) // expected no error
				return
			}
			is.True(err != nil) // expected an error
			ce, ok := conduiterr.Get(err)
			is.True(ok) // expected a coded error
			is.Equal(ce.Code, tc.wantCode)
			is.True(ce.Suggestion != "")
			if tc.wantPath != "" {
				is.True(strings.Contains(ce.Message, tc.wantPath)) // message must name the path
			}
		})
	}
}

func TestJSProcessor_CyclicOutput(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantCode conduiterr.Code
		wantPath string
	}{
		{
			name:     "payload after holds itself",
			script:   `function process(r) { r.Payload.After["self"] = r.Payload.After; return r; }`,
			wantCode: CodeJSCyclicOutput,
			wantPath: ".Payload.After.self",
		},
		{
			name:     "new js object holds itself",
			script:   `function process(r) { var a = {}; a.me = a; r.Payload.After = a; return r; }`,
			wantCode: CodeJSCyclicOutput,
			wantPath: ".Payload.After.me",
		},
		{
			name:     "key holds itself through an array",
			script:   `function process(r) { var k = {}; k.list = [k]; r.Key = k; return r; }`,
			wantCode: CodeJSCyclicOutput,
			wantPath: ".Key.list[0]",
		},
		{
			name: "deeper than the limit",
			script: `function process(r) {
				var o = {}; var c = o;
				for (var i = 0; i < 20000; i++) { c.n = {}; c = c.n; }
				r.Payload.After = o; return r;
			}`,
			wantCode: CodeJSOutputLimitExceeded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			underTest := newTestJavaScriptProc(t, tc.script)

			rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}}
			got := underTest.Process(context.Background(), []opencdc.Record{rec})
			is.Equal(1, len(got))
			errRec, ok := got[0].(sdk.ErrorRecord)
			is.True(ok) // expected an ErrorRecord, not a record that would crash Clone
			ce, ok := conduiterr.Get(errRec.Error)
			is.True(ok) // expected a coded error
			is.Equal(ce.Code, tc.wantCode)
			if tc.wantPath != "" {
				is.True(strings.Contains(ce.Message, tc.wantPath))
			}
		})
	}
}

func TestJSProcessor_SharedValueIsNotACycle(t *testing.T) {
	is := is.New(t)
	underTest := newTestJavaScriptProc(t, `function process(r) {
		var shared = {x: 1};
		r.Payload.After["p"] = shared;
		r.Payload.After["q"] = shared;
		r.Payload.After["l"] = [shared, shared];
		return r;
	}`)
	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}}
	got := underTest.Process(context.Background(), []opencdc.Record{rec})
	is.Equal(1, len(got))
	single, ok := got[0].(sdk.SingleRecord)
	is.True(ok) // a DAG is valid output
	after := single.Payload.After.(opencdc.StructuredData)
	is.Equal(map[string]any{"x": int64(1)}, after["p"])
}

// TestJSProcessor_CyclicOutputDoesNotCrashProcess is the regression test for
// ConduitIO/conduit#3148. Before the fix the record came back as a
// SingleRecord holding a cyclic map, and the next Record.Clone died with
// "fatal error: stack overflow". That is not a panic: it kills the whole test
// binary, so the scenario runs in a child process and the parent asserts on
// how the child exited.
func TestJSProcessor_CyclicOutputDoesNotCrashProcess(t *testing.T) {
	if os.Getenv("CONDUIT_JS_CYCLE_CHILD") == "1" {
		runCyclicChild(t)
		return
	}

	scripts := map[string]string{
		"self assignment": `function process(r) { r.Payload.After["self"] = r.Payload.After; return r; }`,
		"fresh object":    `function process(r) { var a = {}; a.me = a; r.Payload.After = a; return r; }`,
		// Not cyclic: nesting alone overflows Record.Clone (about 2M levels
		// exhaust the 1 GB goroutine stack limit; 1M still fits).
		"deep nesting": `function process(r) {
			var o = {}; var c = o;
			for (var i = 0; i < 2000000; i++) { c.n = {}; c = c.n; }
			r.Payload.After = o; return r;
		}`,
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			if name == "deep nesting" && testing.Short() {
				t.Skip("builds a 2M-level object, about 2 GB of memory")
			}
			cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestJSProcessor_CyclicOutputDoesNotCrashProcess$", "-test.v")
			cmd.Env = append(cmd.Environ(), "CONDUIT_JS_CYCLE_CHILD=1", "CONDUIT_JS_CYCLE_SCRIPT="+script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				tail := string(out)
				if len(tail) > 1500 {
					tail = tail[:1500]
				}
				t.Fatalf("child process died instead of returning an ErrorRecord: %v\n%s", err, tail)
			}
			if !strings.Contains(string(out), "CHILD-OK") {
				t.Fatalf("child did not reach its checks:\n%s", out)
			}
		})
	}
}

func runCyclicChild(t *testing.T) {
	p := NewJavascriptProcessor(log.Nop())
	ctx := context.Background()
	if err := p.Configure(ctx, config.Config{"script": os.Getenv("CONDUIT_JS_CYCLE_SCRIPT")}); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": 1}}}
	got := p.Process(ctx, []opencdc.Record{rec})

	// What every downstream consumer does with a processed record.
	for _, g := range got {
		if single, ok := g.(sdk.SingleRecord); ok {
			_ = opencdc.Record(single).Clone() // fatal stack overflow without the guard
		}
	}
	if _, ok := got[0].(sdk.ErrorRecord); !ok {
		t.Fatalf("expected an ErrorRecord, got %T", got[0])
	}
	t.Log("CHILD-OK")
}
