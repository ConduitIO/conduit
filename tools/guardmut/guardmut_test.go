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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guardedSrc = `package fx

import "errors"

// guard: covered tests=^TestNegative$
func Check(n int) error {
	// guard: neg-input tests=^Test(Negative|Positive)$
	if n < 0 {
		return errors.New("negative")
	}
	// guard: log-call tests=^Test(Negative|Positive)$
	note(n)
	return nil
}

func note(int) {}
`

// fixture writes a throwaway module with the given test file and returns its root.
func fixture(t *testing.T, src, testSrc string) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fx\n\ngo 1.22\n")
	write("fx/fx.go", src)
	write("fx/fx_test.go", testSrc)
	return root
}

func TestMarkerErrors(t *testing.T) {
	for name, src := range map[string]string{
		"not a statement": "package fx\n\n// guard: g\nfunc F() {}\n",
		"assignment":      "package fx\n\nfunc F() {\n\t// guard: g\n\tx := 1\n\t_ = x\n}\n",
		"bad option":      "package fx\n\nfunc F(a bool) {\n\t// guard: g nope=1\n\tif a {\n\t}\n}\n",
		"no name":         "package fx\n\nfunc F(a bool) {\n\t// guard:\n\tif a {\n\t}\n}\n",
		"detached":        "package fx\n\nfunc F(a bool) {\n\t// guard: g\n\n\tif a {\n\t}\n}\n",
		"duplicate":       "package fx\n\nfunc F(a bool) {\n\t// guard: g\n\tif a {\n\t}\n\t// guard: g\n\tif a {\n\t}\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := fixture(t, src, "package fx\n")
			if _, err := Scan(root, []string{"fx"}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEnvOption(t *testing.T) {
	src := "package fx\n\nfunc F(a bool) {\n\t// guard: g tests=^TestF$ env=FX_ON=1\n\tif a {\n\t}\n}\n"
	root := fixture(t, src, "package fx\n")
	gs, err := Scan(root, []string{"fx"})
	if err != nil || len(gs[0].Env) != 1 || gs[0].Env[0] != "FX_ON=1" {
		t.Fatalf("env option not parsed: %v %+v", err, gs)
	}
}

func TestMutateGolden(t *testing.T) {
	src := strings.Replace(guardedSrc, "// guard: covered tests=^TestNegative$\n", "", 1)
	root := fixture(t, src, "package fx\n")
	guards, err := Scan(root, []string{"fx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 2 {
		t.Fatalf("want 2 guards, got %d", len(guards))
	}
	var buf bytes.Buffer
	for _, g := range guards {
		out, err := Mutate(root, g)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString("// " + g.Name + "\n")
		buf.Write(out)
	}
	want := mutantsGolden(t)
	if buf.String() != want {
		t.Fatalf("mutants differ from testdata/mutants.golden\n--- got\n%s\n--- want\n%s", buf.String(), want)
	}
}

func mutantsGolden(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/mutants.golden")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const coveringTest = `package fx

import "testing"

func TestNegative(t *testing.T) {
	if Check(-1) == nil {
		t.Fatal("negative accepted")
	}
}
`

const blindTest = `package fx

import "testing"

func TestPositive(t *testing.T) {
	if err := Check(1); err != nil {
		t.Fatal(err)
	}
}
`

func runGate(t *testing.T, testSrc, baseline string) (code int, out string) {
	t.Helper()
	src := strings.Replace(guardedSrc, "// guard: covered tests=^TestNegative$\n", "", 1)
	src = strings.Replace(src, "\t// guard: log-call tests=^Test(Negative|Positive)$\n\tnote(n)\n", "", 1)
	root := fixture(t, src, testSrc)
	args := []string{"-root", root, "-dirs", "fx", "-json"}
	if baseline != "" {
		bf := filepath.Join(t.TempDir(), "baseline.txt")
		if err := os.WriteFile(bf, []byte(baseline), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-baseline", bf)
	}
	var so, se bytes.Buffer
	code = run(args, &so, &se)
	t.Log(se.String())
	return code, so.String()
}

type jsonOut struct {
	Results []struct {
		Guard   struct{ Name string }
		Status  string
		Verdict string
	}
	Failures []string
}

func TestKilledGuardPasses(t *testing.T) {
	code, out := runGate(t, coveringTest, "")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var j jsonOut
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Results) != 1 || j.Results[0].Status != "killed" {
		t.Fatalf("want one killed guard, got %+v", j.Results)
	}
}

// TestSurvivorIsReported is the point of the tool: a test that does not
// notice the guard's removal must fail the gate.
func TestSurvivorIsReported(t *testing.T) {
	code, out := runGate(t, blindTest, "")
	if code != 1 {
		t.Fatalf("want exit 1, got %d\n%s", code, out)
	}
	var j jsonOut
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		t.Fatal(err)
	}
	if j.Results[0].Status != "survived" || j.Results[0].Verdict != "NEW SURVIVOR" {
		t.Fatalf("want a NEW SURVIVOR, got %+v", j.Results[0])
	}
}

func TestBaselinedSurvivorPassesAndStaleEntryFails(t *testing.T) {
	if code, out := runGate(t, blindTest, "neg-input | fixture: no test on purpose\n"); code != 0 {
		t.Fatalf("baselined survivor should pass, exit %d\n%s", code, out)
	}
	if code, out := runGate(t, coveringTest, "neg-input | stale\n"); code != 1 || !strings.Contains(out, "remove it from the baseline") {
		t.Fatalf("stale baseline entry should fail, exit %d\n%s", code, out)
	}
	if code, _ := runGate(t, blindTest, "no-such-guard | x\n"); code != 1 {
		t.Fatalf("baseline entry for an unknown guard should fail, exit %d", code)
	}
}

func TestTestsFilterMatchingNothingSurvives(t *testing.T) {
	src := strings.Replace(guardedSrc, "tests=^Test(Negative|Positive)$", "tests=TestDoesNotExist", 1)
	src = strings.Replace(src, "// guard: covered tests=^TestNegative$\n", "", 1)
	src = strings.Replace(src, "\t// guard: log-call tests=^Test(Negative|Positive)$\n\tnote(n)\n", "", 1)
	root := fixture(t, src, coveringTest)
	var so, se bytes.Buffer
	if code := run([]string{"-root", root, "-dirs", "fx", "-json"}, &so, &se); code != 1 {
		t.Fatalf("want exit 1 (no test ran), got %d\n%s%s", code, so.String(), se.String())
	}
}

func TestMarkerWithoutTestsIsRejected(t *testing.T) {
	src := strings.Replace(guardedSrc, "neg-input tests=^Test(Negative|Positive)$", "neg-input", 1)
	src = strings.Replace(src, "// guard: covered tests=^TestNegative$\n", "", 1)
	src = strings.Replace(src, "\t// guard: log-call tests=^Test(Negative|Positive)$\n\tnote(n)\n", "", 1)
	root := fixture(t, src, coveringTest)
	var so, se bytes.Buffer
	if code := run([]string{"-root", root, "-dirs", "fx"}, &so, &se); code != 2 || !strings.Contains(se.String(), "needs tests=") {
		t.Fatalf("a marker without tests= must be rejected (exit 2), got %d\n%s", code, se.String())
	}
}

// A package-level failure (here a panic in init that only the mutant triggers)
// is not a kill: no named test failed, so nothing proves a test needs the guard.
func TestPackageLevelFailureIsNotAKill(t *testing.T) {
	src := "package fx\n\nimport \"errors\"\n\nfunc Check(n int) error {\n\t// guard: neg tests=^TestNegative$\n\tif n < 0 {\n\t\treturn errors.New(\"negative\")\n\t}\n\treturn nil\n}\n\nfunc init() {\n\tif Check(-1) == nil {\n\t\tpanic(\"guard missing\")\n\t}\n}\n"
	root := fixture(t, src, coveringTest)
	var so, se bytes.Buffer
	code := run([]string{"-root", root, "-dirs", "fx", "-json"}, &so, &se)
	if code != 1 || !strings.Contains(so.String(), "package-level failure") {
		t.Fatalf("want exit 1 with a package-level failure reason, got %d\n%s%s", code, so.String(), se.String())
	}
}

func TestGuardListRatchet(t *testing.T) {
	src := strings.Replace(guardedSrc, "// guard: covered tests=^TestNegative$\n", "", 1)
	src = strings.Replace(src, "\t// guard: log-call tests=^Test(Negative|Positive)$\n\tnote(n)\n", "", 1)
	root := fixture(t, src, coveringTest)
	list := filepath.Join(t.TempDir(), "guards.txt")
	try := func(content string) (int, string) {
		if err := os.WriteFile(list, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		var so, se bytes.Buffer
		code := run([]string{"-root", root, "-dirs", "fx", "-guards", list}, &so, &se)
		return code, so.String() + se.String()
	}
	if code, out := try("neg-input\n"); code != 0 {
		t.Fatalf("a matching list passes, got %d\n%s", code, out)
	}
	// The marker was deleted from the code but the list still names it.
	if code, out := try("neg-input\ndeleted-guard\n"); code != 1 || !strings.Contains(out, "deleted or renamed") {
		t.Fatalf("a listed guard with no marker must fail, got %d\n%s", code, out)
	}
	// A new marker that nobody listed.
	if code, out := try("\n"); code != 1 || !strings.Contains(out, "not in guards.txt") {
		t.Fatalf("an unlisted marker must fail, got %d\n%s", code, out)
	}
}

// A partial run (-only) must judge the baseline against the full scan: a valid
// entry for a guard that was not selected is neither stale nor unknown.
func TestPartialRunJudgesAgainstFullScan(t *testing.T) {
	src := strings.Replace(guardedSrc, "// guard: covered tests=^TestNegative$\n", "", 1)
	root := fixture(t, src, coveringTest)
	bf := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(bf, []byte("log-call | fixture: note() has no observable effect\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	code := run([]string{"-root", root, "-dirs", "fx", "-only", "neg-input", "-baseline", bf}, &so, &se)
	if code != 0 {
		t.Fatalf("a partial run must not call a valid baseline entry unknown, got %d\n%s%s", code, so.String(), se.String())
	}
	if err := os.WriteFile(bf, []byte("nope | x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	so.Reset()
	if code := run([]string{"-root", root, "-dirs", "fx", "-only", "neg-input", "-baseline", bf}, &so, &se); code != 1 {
		t.Fatalf("an entry that matches no marker anywhere must still fail, got %d", code)
	}
}

func TestFailingBaselineAborts(t *testing.T) {
	failing := "package fx\n\nimport \"testing\"\n\nfunc TestNegative(t *testing.T) { t.Fatal(\"x\") }\n"
	if code, _ := runGate(t, failing, ""); code != 2 {
		t.Fatalf("a failing unmutated baseline must abort with 2, got %d", code)
	}
}

func TestCallGuardRemoved(t *testing.T) {
	src := "package fx\n\nvar Calls int\n\nfunc F() {\n\t// guard: count tests=^TestF$\n\tcount()\n}\n\nfunc count() { Calls++ }\n"
	test := "package fx\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tF()\n\tif Calls != 1 {\n\t\tt.Fatal(Calls)\n\t}\n}\n"
	root := fixture(t, src, test)
	var so, se bytes.Buffer
	if code := run([]string{"-root", root, "-dirs", "fx"}, &so, &se); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, so.String(), se.String())
	}
}

func TestEnvFilterSplitsGuards(t *testing.T) {
	gs := []*Guard{{Name: "a", Env: []string{"MODE=1"}}, {Name: "b"}, {Name: "c", Env: []string{"OTHER=1"}}}
	names := func(in []*Guard) string {
		var out []string
		for _, g := range in {
			out = append(out, g.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(filterEnv(gs, "MODE", "")); got != "a" {
		t.Fatalf("with-env: %s", got)
	}
	if got := names(filterEnv(gs, "", "MODE")); got != "b,c" {
		t.Fatalf("without-env: %s", got)
	}
}
