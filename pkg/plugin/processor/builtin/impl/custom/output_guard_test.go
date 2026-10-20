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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/dop251/goja"
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

// sharedBlowup returns a value whose expanded size is about 3*2^k entries
// while it holds only k+1 distinct containers: s0 = [1], s(i+1) = [s(i), s(i)].
func sharedBlowup(k int) any {
	var v any = []any{1}
	for i := 0; i < k; i++ {
		v = []any{v, v}
	}
	return v
}

// TestOutputChecker_SharedBudget: the entry limit is per record, not per
// field. Each value below is under the limit on its own; together they are
// over it.
func TestOutputChecker_SharedBudget(t *testing.T) {
	// sharedBlowup(21) expands to 3*2^21-2, about 6.3M entries; the limit is
	// 16.7M. Two fit, three do not.
	v := sharedBlowup(21)

	t.Run("each alone is under the limit", func(t *testing.T) {
		is := is.New(t)
		is.NoErr(checkOutput(".Key", v))
	})
	t.Run("two together fit", func(t *testing.T) {
		is := is.New(t)
		c := newOutputChecker()
		is.NoErr(c.check(".Key", v))
		is.NoErr(c.check(".Payload.Before", v))
	})
	t.Run("three together do not", func(t *testing.T) {
		is := is.New(t)
		c := newOutputChecker()
		is.NoErr(c.check(".Key", v))
		is.NoErr(c.check(".Payload.Before", v))
		err := c.check(".Payload.After", v)
		is.True(err != nil) // expected the joint budget to be exceeded
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeJSOutputLimitExceeded)
		is.True(strings.Contains(ce.Message, ".Payload.After"))
	})
}

// TestJSProcessor_OutputLimitIsPerRecord drives the same scenario through
// toSDKRecords, so it fails if the three calls stop sharing one budget.
func TestJSProcessor_OutputLimitIsPerRecord(t *testing.T) {
	p := NewJavascriptProcessor(log.Nop())
	v := map[string]any{"v": sharedBlowup(21)}
	rt := goja.New()

	tests := []struct {
		name     string
		rec      *jsRecord
		wantCode conduiterr.Code
	}{
		{name: "two fields", rec: newBudgetRecord(v, v, nil)},
		{name: "three fields", rec: newBudgetRecord(v, v, v), wantCode: CodeJSOutputLimitExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			_, err := p.toSDKRecords(rt.ToValue(tc.rec))
			if tc.wantCode.IsZero() {
				is.NoErr(err)
				return
			}
			is.True(err != nil) // expected the record to be rejected
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, tc.wantCode)
		})
	}
}

func newBudgetRecord(key, before, after map[string]any) *jsRecord {
	r := &jsRecord{Operation: "create"}
	if key != nil {
		r.Key = key
	}
	if before != nil {
		r.Payload.Before = before
	}
	if after != nil {
		r.Payload.After = after
	}
	return r
}

// TestCheckOutput_FastPathParity: the type-switch fast path and the
// reflection fallback must agree. The cases mix shapes the fast path handles
// (map[string]any, []any, StructuredData) with ones only reflection sees
// (named map and slice types, pointers to maps), including cycles that cross
// from one path to the other, and the false-positive shapes.
func TestCheckOutput_FastPathParity(t *testing.T) {
	type namedMap map[string]any // not handled by the type switch
	type namedSlice []any

	selfFast := map[string]any{}
	selfFast["me"] = selfFast
	selfNamed := namedMap{}
	selfNamed["me"] = selfNamed

	// A cycle that crosses both paths: fast map -> pointer to the same map.
	viaPtr := map[string]any{}
	viaPtr["me"] = &viaPtr

	structured := opencdc.StructuredData{}
	structured["list"] = []any{structured}

	s := []any{1, 2}
	sharedSlice := []any{s, s[:1], s, s[:1]} // same backing array, two lengths: a DAG, not a cycle
	namedShared := namedSlice{s, s[:1], s, s[:1]}

	sub := map[string]any{"x": 1}
	nilMap := map[string]any(nil)
	var nilPtr *map[string]any
	var nilRaw *opencdc.RawData

	tests := []struct {
		name   string
		in     any
		cyclic bool
	}{
		{name: "self fast", in: selfFast, cyclic: true},
		{name: "self named", in: selfNamed, cyclic: true},
		{name: "self via pointer", in: viaPtr, cyclic: true},
		{name: "structured data cycle", in: structured, cyclic: true},
		{name: "same backing array different lengths", in: sharedSlice},
		{name: "same backing array different lengths, named", in: namedShared},
		{name: "same map under many parents", in: map[string]any{"a": sub, "b": []any{sub, sub}, "c": namedMap{"d": sub}}},
		{name: "typed nil map", in: map[string]any{"a": nilMap}},
		{name: "typed nil pointers", in: map[string]any{"a": nilPtr, "b": nilRaw}},
		{name: "empty containers", in: []any{map[string]any{}, []any{}, opencdc.StructuredData{}, namedMap{}}},
		{name: "raw data and bytes", in: map[string]any{"a": opencdc.RawData("x"), "b": []byte("y"), "c": func() any { r := opencdc.RawData("z"); return &r }()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := checkOutput("", tc.in)
			if !tc.cyclic {
				is.NoErr(err)
				return
			}
			ce, ok := conduiterr.Get(err)
			is.True(ok) // expected a coded error
			is.Equal(ce.Code, CodeJSCyclicOutput)
		})
	}
}

func benchRecordData() map[string]any {
	m := make(map[string]any, 20)
	for i := 0; i < 20; i++ {
		m[fmt.Sprintf("field%d", i)] = i
	}
	return m
}

// BenchmarkCheckOutput measures the guard alone. Indicative only (a laptop
// micro-benchmark, not a benchi run).
func BenchmarkCheckOutput(b *testing.B) {
	nested := map[string]any{
		"a": benchRecordData(),
		"b": []any{benchRecordData(), benchRecordData()},
		"c": map[string]any{"d": []any{1, "x", 2.5, true, nil}},
	}
	for _, bc := range []struct {
		name string
		in   any
	}{
		{"flat20", benchRecordData()},
		{"structured20", opencdc.StructuredData(benchRecordData())},
		{"nested", nested},
		{"rawdata", opencdc.RawData("payload")},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := checkOutput(".Payload.After", bc.in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestCheckOutput_DepthOfSharedContainers: a container shared by several
// parents is walked once, from wherever it was first seen. The depth limit
// must still hold at every place it appears.
func TestCheckOutput_DepthOfSharedContainers(t *testing.T) {
	// A chain of N nodes, n0 -> n1 -> ... -> nN-1, with the root listing
	// every step-th node in reverse order. Each listed node is walked for at
	// most step levels before it meets the previously listed (cached) one, so
	// no first walk is deep; yet the last listed node really nests about N
	// levels.
	const (
		nodes = 560_000
		step  = 9_824
	)
	// One key per node keeps the expanded size just under the entry limit
	// (about 16.6M of 16.77M), so only the depth check can reject this.
	chain := make([]map[string]any, nodes)
	for i := range chain {
		chain[i] = map[string]any{}
		if i > 0 {
			chain[i-1]["next"] = chain[i]
		}
	}
	chain[nodes-1]["leaf"] = 1
	var root []any
	for i := nodes - 1; i >= 0; i -= step {
		root = append(root, chain[i])
	}

	is := is.New(t)
	err := checkOutput("", root)
	is.True(err != nil) // a 560k-level nesting must be rejected however it is reached
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeJSOutputLimitExceeded)

	// A shared container at the limit exactly is still fine.
	shared := chainOf(maxOutputDepth - 1) // height maxOutputDepth-1, so depth 1 reaches the limit
	is.NoErr(checkOutput("", []any{shared, shared}))
	is.True(checkOutput("", []any{[]any{shared}, shared}) != nil) // one level deeper than the limit
}

// chainOf returns n nested maps (height n).
func chainOf(n int) map[string]any {
	root := map[string]any{}
	cur := root
	for i := 1; i < n; i++ {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	cur["leaf"] = 1
	return root
}

// TestCheckOutput_DepthViaReflection: the depth limit on the reflection
// fallback (named map types) matches the fast path.
func TestCheckOutput_DepthViaReflection(t *testing.T) {
	type namedMap map[string]any
	build := func(n int) any {
		root := namedMap{}
		cur := root
		for i := 1; i < n; i++ {
			next := namedMap{}
			cur["n"] = next
			cur = next
		}
		cur["leaf"] = 1
		return root
	}
	is := is.New(t)
	is.NoErr(checkOutput("", build(maxOutputDepth)))
	err := checkOutput("", build(maxOutputDepth+1))
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeJSOutputLimitExceeded)
}
