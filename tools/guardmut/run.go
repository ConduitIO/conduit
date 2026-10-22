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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Status is the outcome for one guard.
type Status string

const (
	// StatusKilled means the tests failed with the guard removed: the guard is covered.
	StatusKilled Status = "killed"
	// StatusSurvived means the tests passed (or none ran) with the guard removed.
	StatusSurvived Status = "survived"
	// StatusError means the mutant could not be judged (did not compile, timed out
	// before any verdict, not run within the budget). It fails the gate.
	StatusError Status = "error"
)

// Result is the verdict for one guard.
type Result struct {
	Guard     *Guard  `json:"guard"`
	Status    Status  `json:"status"`
	Reason    string  `json:"reason,omitempty"`
	KilledBy  string  `json:"killed_by,omitempty"`
	Seconds   float64 `json:"seconds"`
	Baselined bool    `json:"baselined,omitempty"`
	Verdict   string  `json:"verdict"` // ok | NEW SURVIVOR | STALE BASELINE | ERROR
}

// Runner runs mutants.
type Runner struct {
	Root     string
	Tags     string
	Timeout  time.Duration // per go test invocation
	Parallel int
	Budget   time.Duration // overall; 0 means none
	TmpDir   string
	Log      func(format string, args ...any)
}

type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type outcome struct {
	failed     bool
	passes     int
	failedTest string
	buildFail  bool
	output     string
}

func (r *Runner) goTest(ctx context.Context, g *Guard, overlay string) (outcome, error) {
	args := []string{"test", "-json", "-count=1", "-timeout", r.Timeout.String()}
	if r.Tags != "" {
		args = append(args, "-tags", r.Tags)
	}
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	if g.Tests != "" {
		args = append(args, "-run", g.Tests)
	}
	args = append(args, g.Pkgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = r.Root
	// PWD must name the directory we run in: with an explicit Env, exec does
	// not set it, and the go command resolves the working directory (and so
	// the overlay's paths) from an inherited, stale PWD.
	absRoot, _ := filepath.Abs(r.Root)
	cmd.Env = append(append(os.Environ(), g.Env...), "PWD="+absRoot)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	var o outcome
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		var ev testEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			tail = append(tail, line)
			if strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]") {
				o.buildFail = true
			}
			continue
		}
		if strings.Contains(ev.Output, "[build failed]") || strings.Contains(ev.Output, "[setup failed]") || ev.Action == "build-fail" {
			o.buildFail = true
		}
		switch ev.Action {
		case "pass":
			if ev.Test != "" {
				o.passes++
			}
		case "fail":
			o.failed = true
			if ev.Test != "" && o.failedTest == "" {
				o.failedTest = ev.Test
			}
		case "output":
			tail = append(tail, strings.TrimRight(ev.Output, "\n"))
		}
	}
	if len(tail) > 40 {
		tail = tail[len(tail)-40:]
	}
	o.output = strings.Join(tail, "\n")
	if err != nil && !o.failed && !o.buildFail {
		// go test exited non-zero without a fail event: treat as failure
		// of the run itself (for example the toolchain could not start).
		if ctx.Err() != nil {
			return o, ctx.Err()
		}
		o.failed = true
	}
	return o, nil
}

// Baseline runs each distinct (packages, -run) set once, unmutated. A failing
// baseline aborts: every mutant would look killed.
func (r *Runner) Baseline(ctx context.Context, guards []*Guard) error {
	done := map[string]bool{}
	for _, g := range guards {
		key := g.Tests + "|" + strings.Join(g.Pkgs, ",")
		if done[key] {
			continue
		}
		done[key] = true
		r.Log("baseline %s", strings.Join(g.Pkgs, " "))
		o, err := r.goTest(ctx, g, "")
		if err != nil {
			return err
		}
		if o.failed || o.buildFail {
			return fmt.Errorf("baseline (no mutation) fails for %s, fix the tests first:\n%s", strings.Join(g.Pkgs, " "), o.output)
		}
		if o.passes == 0 {
			r.Log("warning: baseline for %s ran no passing tests; guard %s can only survive", strings.Join(g.Pkgs, " "), g.Name)
		}
	}
	return nil
}

// Run mutates and tests every guard.
func (r *Runner) Run(ctx context.Context, guards []*Guard) []*Result {
	results := make([]*Result, len(guards))
	var deadline time.Time
	if r.Budget > 0 {
		deadline = time.Now().Add(r.Budget)
	}
	sem := make(chan struct{}, max(1, r.Parallel))
	var wg sync.WaitGroup
	for i, g := range guards {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = r.runOne(ctx, g, deadline)
		}()
	}
	wg.Wait()
	return results
}

func (r *Runner) runOne(ctx context.Context, g *Guard, deadline time.Time) *Result {
	res := &Result{Guard: g}
	start := time.Now()
	defer func() { res.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds() }()
	if !deadline.IsZero() && time.Now().After(deadline) {
		res.Status, res.Reason = StatusError, "not run: overall budget exhausted"
		return res
	}
	mut, err := Mutate(r.Root, g)
	if err != nil {
		res.Status, res.Reason = StatusError, err.Error()
		return res
	}
	dir, err := os.MkdirTemp(r.TmpDir, "guardmut-")
	if err != nil {
		res.Status, res.Reason = StatusError, err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	mfile := filepath.Join(dir, "mutant.go")
	if err := os.WriteFile(mfile, mut, 0o600); err != nil {
		res.Status, res.Reason = StatusError, err.Error()
		return res
	}
	abs, _ := filepath.Abs(filepath.Join(r.Root, filepath.FromSlash(g.File)))
	ov, _ := json.Marshal(map[string]map[string]string{"Replace": {abs: mfile}})
	ovfile := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(ovfile, ov, 0o600); err != nil {
		res.Status, res.Reason = StatusError, err.Error()
		return res
	}
	r.Log("mutating %s (%s:%d)", g.Name, g.File, g.Line)
	o, err := r.goTest(ctx, g, ovfile)
	switch {
	case err != nil:
		res.Status, res.Reason = StatusError, err.Error()
	case o.buildFail:
		res.Status, res.Reason = StatusError, "mutant does not compile (not viable): "+firstLine(o.output)
	case o.failed:
		// A kill counts only when a named test that tests= selects failed. A package-level
		// failure (panic in init, TestMain, timeout, crash) says nothing about the guard.
		if o.failedTest == "" {
			res.Status = StatusError
			res.Reason = "killed only by a package-level failure (panic, timeout, TestMain), not by a test matching tests=" + g.Tests
		} else if !selectedByTests(g.Tests, o.failedTest) {
			res.Status = StatusError
			res.Reason = fmt.Sprintf("killed by %s, which tests=%s does not select", o.failedTest, g.Tests)
		} else {
			res.Status, res.KilledBy = StatusKilled, o.failedTest
		}
	case o.passes == 0:
		res.Status, res.Reason = StatusSurvived, "no test ran (tests= matched nothing, or every test skipped)"
	default:
		res.Status, res.Reason = StatusSurvived, fmt.Sprintf("%d tests passed with the guard removed", o.passes)
	}
	return res
}

// selectedByTests reports whether test (as `go test -json` names it, with
// "/" between subtests) is selected by the -run expression: the top-level
// name must match the expression's first "/"-separated element.
func selectedByTests(expr, test string) bool {
	first, _, _ := strings.Cut(expr, "/")
	top, _, _ := strings.Cut(test, "/")
	re, err := regexp.Compile(first)
	return err == nil && re.MatchString(top)
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "FAIL") {
			return strings.TrimSpace(l)
		}
	}
	return s
}
