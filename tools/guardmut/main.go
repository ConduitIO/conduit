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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Baseline maps a guard name to the written reason it is allowed to survive.
type Baseline map[string]string

// LoadBaseline reads lines of `name | justification`. A missing file is an
// empty baseline. An entry without a justification is an error.
func LoadBaseline(path string) (Baseline, error) {
	b := Baseline{}
	if path == "" {
		return b, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, why, ok := strings.Cut(line, "|")
		name, why = strings.TrimSpace(name), strings.TrimSpace(why)
		if !ok || why == "" {
			return nil, fmt.Errorf("%s:%d: baseline entry %q needs `name | justification`", path, n, name)
		}
		b[name] = why
	}
	return b, sc.Err()
}

// Judge applies the ratchet: a survivor fails unless baselined, a baselined
// guard that is now killed fails (shrink the list), a baseline name that
// matches no guard fails. It returns the failure messages.
//
// all is the set of every guard name in the FULL scan. A partial run (-only,
// or -dirs narrower than -all-dirs) executes a subset, but a baseline entry
// is "unknown" only when no marker anywhere carries its name, so a partial run
// cannot report a valid entry as stale or hide a missing one.
func Judge(results []*Result, base Baseline, all map[string]bool) []string {
	var fails []string
	for _, r := range results {
		_, inBase := base[r.Guard.Name]
		r.Baselined = inBase
		switch {
		case r.Status == StatusError:
			r.Verdict = "ERROR"
			fails = append(fails, fmt.Sprintf("%s (%s:%d): %s", r.Guard.Name, r.Guard.File, r.Guard.Line, r.Reason))
		case r.Status == StatusSurvived && !inBase:
			r.Verdict = "NEW SURVIVOR"
			fails = append(fails, fmt.Sprintf("%s (%s:%d) survived: %s. Add a test that fails without the guard, or (with a written reason) a baseline entry", r.Guard.Name, r.Guard.File, r.Guard.Line, r.Reason))
		case r.Status == StatusKilled && inBase:
			r.Verdict = "STALE BASELINE"
			fails = append(fails, fmt.Sprintf("%s is now killed by %s: remove it from the baseline", r.Guard.Name, r.KilledBy))
		case inBase:
			r.Verdict = "ok (baselined survivor)"
		default:
			r.Verdict = "ok"
		}
	}
	names := make([]string, 0, len(base))
	for name := range base {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !all[name] {
			fails = append(fails, fmt.Sprintf("baseline entry %q matches no guard marker: remove it", name))
		}
	}
	return fails
}

// CheckGuardList compares the committed list of guard names with the markers
// found in the full scan, in both directions: a listed name with no marker
// means a guard was deleted or renamed (the list is how deleting a marker
// fails the gate), and a marker not in the list must be added deliberately.
func CheckGuardList(path string, all map[string]bool) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("guard list: %w", err)
	}
	defer f.Close()
	listed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !nameRE.MatchString(line) {
			return nil, fmt.Errorf("%s:%d: %q is not a guard name", path, n, line)
		}
		if listed[line] {
			return nil, fmt.Errorf("%s:%d: %q is listed twice", path, n, line)
		}
		listed[line] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var fails []string
	var names []string
	for n := range listed {
		names = append(names, n)
	}
	for n := range all {
		if !listed[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		switch {
		case listed[n] && !all[n]:
			fails = append(fails, fmt.Sprintf("guard %q is in %s but no `// guard: %s` marker exists: it was deleted or renamed. Restore the guard, or remove it from the list in a reviewed change", n, filepath.Base(path), n))
		case !listed[n] && all[n]:
			fails = append(fails, fmt.Sprintf("guard %q has a marker but is not in %s: add it", n, filepath.Base(path)))
		}
	}
	return fails, nil
}

// Markdown renders a GitHub step summary.
func Markdown(w io.Writer, results []*Result, fails []string, elapsed time.Duration) {
	fmt.Fprintf(w, "### guardmut\n\n| guard | location | status | detail | s | verdict |\n|---|---|---|---|---|---|\n")
	for _, r := range results {
		detail := r.KilledBy
		if detail == "" {
			detail = r.Reason
		}
		fmt.Fprintf(w, "| `%s` | `%s:%d` | %s | %s | %.0f | %s |\n", r.Guard.Name, r.Guard.File, r.Guard.Line, r.Status, strings.ReplaceAll(detail, "|", "/"), r.Seconds, r.Verdict)
	}
	fmt.Fprintf(w, "\n%d guards, %d failures, %s\n", len(results), len(fails), elapsed.Round(time.Second))
	for _, f := range fails {
		fmt.Fprintf(w, "\n- %s", f)
	}
	fmt.Fprintln(w)
}

// filterEnv keeps guards that set the env key `with` (when given) and do not
// set the key `without` (when given).
func filterEnv(guards []*Guard, with, without string) []*Guard {
	sets := func(g *Guard, key string) bool {
		for _, kv := range g.Env {
			if k, _, _ := strings.Cut(kv, "="); k == key {
				return true
			}
		}
		return false
	}
	var out []*Guard
	for _, g := range guards {
		if with != "" && !sets(g, with) || without != "" && sets(g, without) {
			continue
		}
		out = append(out, g)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range in {
		if d = strings.TrimSpace(d); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// selectGuards keeps the guards whose file is under one of dirs.
func selectGuards(full []*Guard, dirs []string) []*Guard {
	var out []*Guard
	for _, g := range full {
		for _, d := range dirs {
			d = strings.Trim(strings.TrimSpace(d), "/")
			if d != "" && (g.File == d || strings.HasPrefix(g.File, d+"/")) {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("guardmut", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "module root `go test` runs in")
	dirs := fs.String("dirs", "", "comma-separated directories (relative to root) whose guards are run")
	allDirs := fs.String("all-dirs", "", "every directory that carries markers (default: -dirs); the baseline and the guard list are judged against the full scan")
	guardList := fs.String("guards", "", "committed list of every guard name; a missing marker or an unlisted marker fails")
	baselineFile := fs.String("baseline", "", "baseline file of allowed survivors (`name | justification` per line)")
	tags := fs.String("tags", "", "build tags passed to go test")
	timeout := fs.Duration("timeout", 10*time.Minute, "go test timeout per mutant")
	budget := fs.Duration("budget", 0, "overall time budget; guards not reached fail the gate (0 = none)")
	parallel := fs.Int("parallel", 1, "mutants run at once (keep 1 for tests that share a database)")
	only := fs.String("only", "", "run only this guard name")
	withEnv := fs.String("with-env", "", "run only guards whose marker sets this env KEY (for example a mode that changes cluster-wide settings, run alone)")
	withoutEnv := fs.String("without-env", "", "run only guards whose marker does NOT set this env KEY")
	list := fs.Bool("list", false, "list the guards and exit")
	asJSON := fs.Bool("json", false, "write the result as JSON on stdout")
	summary := fs.String("summary", "", "append a markdown summary to this file (for $GITHUB_STEP_SUMMARY)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logf := func(format string, a ...any) { fmt.Fprintf(stderr, "guardmut: "+format+"\n", a...) }
	if *dirs == "" {
		logf("-dirs is required")
		return 2
	}
	scanDirs := *dirs
	if *allDirs != "" {
		scanDirs = *allDirs + "," + *dirs
	}
	full, err := Scan(*root, dedupe(strings.Split(scanDirs, ",")))
	if err != nil {
		logf("%v", err)
		return 2
	}
	all := map[string]bool{}
	for _, g := range full {
		all[g.Name] = true
	}
	guards := selectGuards(full, strings.Split(*dirs, ","))
	if len(guards) == 0 {
		logf("no guard markers found under %s: refusing to pass an empty gate", *dirs)
		return 2
	}
	if *withEnv != "" || *withoutEnv != "" {
		guards = filterEnv(guards, *withEnv, *withoutEnv)
		if len(guards) == 0 {
			logf("no guard matches -with-env=%q -without-env=%q", *withEnv, *withoutEnv)
			return 2
		}
	}
	if *only != "" {
		var sel []*Guard
		for _, g := range guards {
			if g.Name == *only {
				sel = append(sel, g)
			}
		}
		if len(sel) == 0 {
			logf("no guard named %q", *only)
			return 2
		}
		guards = sel
	}
	if *list {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(guards)
			return 0
		}
		for _, g := range guards {
			fmt.Fprintf(stdout, "%s\t%s:%d\t%s\t%s\n", g.Name, g.File, g.Line, g.Kind, strings.Join(g.Pkgs, ","))
		}
		return 0
	}
	base, err := LoadBaseline(*baselineFile)
	if err != nil {
		logf("%v", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := &Runner{Root: *root, Tags: *tags, Timeout: *timeout, Parallel: *parallel, Budget: *budget, TmpDir: os.TempDir(), Log: logf}
	start := time.Now()
	if err := r.Baseline(ctx, guards); err != nil {
		logf("%v", err)
		return 2
	}
	results := r.Run(ctx, guards)
	fails := Judge(results, base, all)
	if *guardList != "" {
		lf, err := CheckGuardList(*guardList, all)
		if err != nil {
			logf("%v", err)
			return 2
		}
		fails = append(fails, lf...)
	}
	elapsed := time.Since(start)
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"results": results, "failures": fails, "seconds": elapsed.Seconds()})
	} else {
		Markdown(stdout, results, fails, elapsed)
	}
	if *summary != "" {
		if f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			Markdown(f, results, fails, elapsed)
			f.Close()
		}
	}
	if len(fails) > 0 {
		return 1
	}
	return 0
}
