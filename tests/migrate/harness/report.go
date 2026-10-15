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
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/conduitio/yaml/v3"
	"github.com/goccy/go-json"
)

// The report contract, as the harness reads it: the example JSON and entry
// fields of the base design ("Output: the compatibility report") and the
// field semantics, determinism and exit-code tables of #3057 ("The
// compatibility report as a contract"). Unknown fields are ignored, as the
// contract tells consumers to do.

// Envelope is the family A --json envelope.
type Envelope struct {
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Summary *Summary        `json:"summary"`
	Result  json.RawMessage `json:"result"`
	Error   *EnvelopeError  `json:"error"`
}

type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Summary struct {
	Connectors   int `json:"connectors"`
	Keys         int `json:"keys"`
	Translated   int `json:"translated"`
	Approximated int `json:"approximated"`
	Unsupported  int `json:"unsupported"`
	Blocking     int `json:"blocking"`
}

// Report is the envelope's result.
type Report struct {
	ReportVersion int               `json:"reportVersion"`
	Generator     map[string]any    `json:"generator"`
	Connectors    []ReportConnector `json:"connectors"`
	AcceptedGaps  json.RawMessage   `json:"acceptedGaps"`
}

type ReportConnector struct {
	Name           string  `json:"name"`
	Class          string  `json:"class"`
	Role           string  `json:"role"`
	Pipeline       string  `json:"pipeline"`
	PipelineStatus string  `json:"pipelineStatus"`
	Entries        []Entry `json:"entries"`
	Notes          []Note  `json:"notes"`
}

type Entry struct {
	Key      string  `json:"key"`
	Status   string  `json:"status"`
	Origin   Origin  `json:"origin"`
	Target   *Target `json:"target"`
	Code     string  `json:"code"`
	Reason   string  `json:"reason"`
	Fix      string  `json:"fix"`
	Blocking bool    `json:"blocking"`
	Secret   bool    `json:"secret"`
}

type Origin struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Pointer string `json:"pointer"`
}

type Target struct {
	ConfigPath string `json:"configPath"`
	Env        string `json:"env"`
}

type Note struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	Fix      string `json:"fix"`
	Blocking bool   `json:"blocking"`
}

const (
	commandMigrate  = "migrate.kafka-connect"
	codeKeyUnknown  = "kc.key_unknown"
	codeKeyDup      = "kc.key_duplicate"
	statusStopped   = "stopped"
	statusRunning   = "running"
	roleWorker      = "worker"
	pipelinesPrefix = "pipelines/"
	reportFile      = "report.json"
)

var (
	envVarRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	// mustBeSecret are keys the base design's secret list covers without
	// doubt; their report entries must say secret: true.
	mustBeSecret = regexp.MustCompile(`(?i)(password|sasl\.jaas\.config|secret\.access\.key)$`)
)

// Check is the input to CheckOutput.
type Check struct {
	Case   Case
	Inputs []InputFile
	Out    Output
	// HostPaths are absolute paths of this run (work dir, output dir, case
	// dir) that must not appear in any output byte.
	HostPaths []string
	// EnvelopeOnly skips the checks that need the CLI's files and exit code
	// (used for the MCP output, which is compared to the CLI's instead).
	EnvelopeOnly bool
}

// Result of CheckOutput: the problems found, the parsed report, and the
// plugins the generated pipelines reference (for the certification gate).
type Result struct {
	Problems []string
	Envelope *Envelope
	Report   *Report
	Plugins  []string
}

// CheckOutput checks one run against the report contract, the
// never-silently-drop property and the case's expectations. It never prints
// an input value.
//
//nolint:gocyclo // one pass over the contract, in the order the design states it
func CheckOutput(c Check) Result {
	var r Result
	add := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }
	m := c.Case.Manifest

	r.Problems = append(r.Problems, scanLeaks(c.Out, c.HostPaths)...)

	var env Envelope
	if err := json.Unmarshal(c.Out.Stdout, &env); err != nil {
		add("stdout is not a JSON envelope: %v", err)
		return r
	}
	r.Envelope = &env
	if !c.EnvelopeOnly {
		if env.Command != commandMigrate {
			add("envelope command is %q, want %q", env.Command, commandMigrate)
		}
		if m.Expect.ExitCode != nil && c.Out.ExitCode != *m.Expect.ExitCode {
			add("exit code %d, want %d", c.Out.ExitCode, *m.Expect.ExitCode)
		}
	}

	// A rejected input: the whole input is refused with a code, nothing is
	// written. That accounts for every key (rejected), so completeness holds.
	if m.Expect.Error != "" {
		switch {
		case env.Error == nil:
			add("input must be rejected with %s, but the envelope has no error", m.Expect.Error)
		case env.Error.Code != m.Expect.Error:
			add("envelope error code %q, want %q", env.Error.Code, m.Expect.Error)
		}
		if !c.EnvelopeOnly {
			if n := countPipelines(c.Out.Files); n != 0 {
				add("a rejected input wrote %d pipeline files", n)
			}
		}
		return r
	}
	if env.Error != nil {
		add("hard failure %s: %s (a corpus input that the command cannot report on is a translator bug)", env.Error.Code, env.Error.Message)
		return r
	}

	var rep Report
	if err := json.Unmarshal(env.Result, &rep); err != nil {
		add("result is not a compatibility report: %v", err)
		return r
	}
	r.Report = &rep
	if rep.ReportVersion != 1 {
		add("reportVersion %d, want 1", rep.ReportVersion)
	}
	if _, ok := rep.Generator["conduit"]; !ok {
		add("generator.conduit is missing")
	}

	accepted := len(rep.AcceptedGaps) > 0 && string(rep.AcceptedGaps) != "null" && string(rep.AcceptedGaps) != "[]"

	// Summary semantics.
	var sum Summary
	approximatedAny := false
	sum.Connectors = len(rep.Connectors)
	blockingAny := false
	for _, con := range rep.Connectors {
		conBlocking := false
		for _, e := range con.Entries {
			sum.Keys++
			switch e.Status {
			case StatusTranslated:
				sum.Translated++
			case StatusApproximated:
				sum.Approximated++
				approximatedAny = true
			case StatusUnsupported:
				sum.Unsupported++
			default:
				add("%s: entry %q has status %q", con.Name, e.Key, e.Status)
			}
			if e.Blocking {
				sum.Blocking++
				conBlocking = true
			}
			r.Problems = append(r.Problems, checkEntry(con.Name, e)...)
		}
		for _, n := range con.Notes {
			if n.Blocking {
				sum.Blocking++
				conBlocking = true
			}
			if n.Code == "" || n.Reason == "" || (n.Blocking && n.Fix == "") {
				add("%s: note %q needs code, reason and (when blocking) fix", con.Name, n.Code)
			}
		}
		blockingAny = blockingAny || conBlocking
		// A paused Kafka Connect connector is written stopped too, so only
		// one direction is a rule: blocking means stopped.
		if con.Role != roleWorker && con.Pipeline != "" && conBlocking && !accepted && con.PipelineStatus != statusStopped {
			add("%s: has blocking entries or notes but pipelineStatus is %q, want %q", con.Name, con.PipelineStatus, statusStopped)
		}
	}
	switch {
	case env.Summary == nil:
		add("envelope summary is missing")
	case *env.Summary != sum:
		add("summary %+v does not match the entries, which give %+v", *env.Summary, sum)
	}
	if wantOK := !blockingAny || accepted; env.OK != wantOK {
		add("ok is %v, want %v (blocking entries or notes: %v)", env.OK, wantOK, blockingAny)
	}
	if !c.EnvelopeOnly {
		// Exit-code matrix (#3057): 2 on a blocking entry or note unless the
		// gaps were accepted; --strict also fails on accepted gaps and on any
		// approximated entry.
		strict := slices.Contains(m.Command.Args, "--strict")
		want := 0
		if (blockingAny && !accepted) || (strict && (accepted || approximatedAny)) {
			want = 2
		}
		if c.Out.ExitCode != want {
			add("exit code %d does not follow the report: want %d", c.Out.ExitCode, want)
		}
	}

	r.Problems = append(r.Problems, checkCompleteness(c.Inputs, rep)...)
	r.Problems = append(r.Problems, checkUnknown(m, rep)...)
	r.Problems = append(r.Problems, checkExpectations(m, rep)...)

	if !c.EnvelopeOnly {
		plugins, problems := checkPipelines(rep, c.Out.Files, m)
		r.Plugins = plugins
		r.Problems = append(r.Problems, problems...)
		r.Problems = append(r.Problems, checkReportFile(env.Result, c.Out.Files)...)
	}
	return r
}

//nolint:gocyclo // a flat list of independent field rules
func checkEntry(con string, e Entry) []string {
	var p []string
	add := func(format string, args ...any) {
		p = append(p, fmt.Sprintf("%s: entry %q: ", con, e.Key)+fmt.Sprintf(format, args...))
	}
	if e.Key == "" {
		add("key is empty")
	}
	if e.Origin.File == "" {
		add("origin.file is empty")
	}
	if strings.HasPrefix(e.Origin.File, "/") || strings.Contains(e.Origin.File, `\`) || (strings.Contains(e.Origin.File, ":") && e.Origin.File != "<stdin>") {
		add("origin.file %q must be relative with forward slashes", e.Origin.File)
	}
	if (e.Origin.Line > 0) == (e.Origin.Pointer != "") {
		add("origin needs exactly one of line or pointer")
	}
	if e.Status != StatusTranslated && e.Reason == "" {
		add("reason is required for %s", e.Status)
	}
	if e.Status == StatusUnsupported && (e.Fix == "" || e.Code == "") {
		add("unsupported entries need code and fix")
	}
	if e.Blocking && e.Fix == "" {
		add("blocking entries need a fix")
	}
	if e.Blocking && e.Status == StatusTranslated {
		add("a translated entry cannot be blocking")
	}
	// A translated secret lands in the pipeline as a variable; an
	// unsupported one (an unknown class) has no target at all.
	if e.Secret && e.Status != StatusUnsupported && (e.Target == nil || !envVarRe.MatchString(e.Target.Env)) {
		add("secret entries that reach the pipeline need target.env naming the variable")
	}
	return p
}

// checkCompleteness is the never-silently-drop property: the multiset of
// input key occurrences equals the multiset of report entries, file by file,
// in input order and at the same location.
func checkCompleteness(inputs []InputFile, rep Report) []string {
	var p []string
	byFile := map[string][]Entry{}
	for _, con := range rep.Connectors {
		for _, e := range con.Entries {
			byFile[e.Origin.File] = append(byFile[e.Origin.File], e)
		}
	}
	for _, f := range inputs {
		got := byFile[f.Label]
		delete(byFile, f.Label)
		want := f.Keys
		if missing, extra := multisetDiff(want, got); len(missing)+len(extra) > 0 {
			if len(missing) > 0 {
				p = append(p, fmt.Sprintf("%s: input keys with no report entry (silently dropped): %v", f.Label, missing))
			}
			if len(extra) > 0 {
				p = append(p, fmt.Sprintf("%s: report entries for keys not in the input: %v", f.Label, extra))
			}
			continue
		}
		for i := range want {
			w, g := want[i], got[i]
			if w.Key != g.Key {
				p = append(p, fmt.Sprintf("%s: entry %d is %q, input order has %q (entries must follow input order)", f.Label, i, g.Key, w.Key))
				break
			}
			if mustBeSecret.MatchString(w.Key) && w.Value != "" && !g.Secret {
				p = append(p, fmt.Sprintf("%s: entry %q carries a credential but is not marked secret", f.Label, g.Key))
			}
			if (w.Line > 0 && g.Origin.Line != w.Line) || (w.Pointer != "" && g.Origin.Pointer != w.Pointer) {
				p = append(p, fmt.Sprintf("%s: entry %q has origin line %d pointer %q, want line %d pointer %q", f.Label, g.Key, g.Origin.Line, g.Origin.Pointer, w.Line, w.Pointer))
			}
		}
	}
	for file, es := range byFile {
		p = append(p, fmt.Sprintf("report entries name origin file %q, which is not an input (%d entries)", file, len(es)))
	}
	sort.Strings(p)
	return p
}

func multisetDiff(want []KeyOccurrence, got []Entry) (missing, extra []string) {
	count := map[string]int{}
	for _, k := range want {
		count[k.Key]++
	}
	for _, e := range got {
		count[e.Key]--
	}
	for k, n := range count {
		for ; n > 0; n-- {
			missing = append(missing, k)
		}
		for ; n < 0; n++ {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func checkUnknown(m Manifest, rep Report) []string {
	if m.Family == FamilyCommon {
		return nil
	}
	var p []string
	for _, con := range rep.Connectors {
		for _, e := range con.Entries {
			if e.Code == codeKeyUnknown && !slices.Contains(m.ExpectUnknown, e.Key) {
				p = append(p, fmt.Sprintf("%s: %q is %s; add a rule for it or list it in expectUnknown", con.Name, e.Key, codeKeyUnknown))
			}
		}
	}
	return p
}

func checkExpectations(m Manifest, rep Report) []string {
	var all []Entry
	var notes []string
	for _, con := range rep.Connectors {
		all = append(all, con.Entries...)
		for _, n := range con.Notes {
			notes = append(notes, n.Code)
		}
	}
	p := checkEntryExpectations(m.Expect.Entries, all)
	for _, n := range m.Expect.Notes {
		if !slices.Contains(notes, n) {
			p = append(p, fmt.Sprintf("expected a connector note %s, got %v", n, notes))
		}
	}
	if c := m.Expect.Counts; c != nil {
		if got := countEntries(all); got != *c {
			p = append(p, fmt.Sprintf("counts %+v, want %+v", got, *c))
		}
	}
	return p
}

func countEntries(all []Entry) Counts {
	var got Counts
	for _, e := range all {
		switch e.Status {
		case StatusTranslated:
			got.Translated++
		case StatusApproximated:
			got.Approximated++
		case StatusUnsupported:
			got.Unsupported++
		}
		if e.Blocking {
			got.Blocking++
		}
	}
	return got
}

// checkEntryExpectations: every entry for an expected key (except the
// overridden kc.key_duplicate occurrences) has the expected classification.
func checkEntryExpectations(expect []EntryExpect, all []Entry) []string {
	var p []string
	for _, want := range expect {
		found := false
		for _, e := range all {
			if e.Key != want.Key || e.Code == codeKeyDup {
				continue
			}
			found = true
			if e.Status != want.Status {
				p = append(p, fmt.Sprintf("entry %q: status %s, want %s", e.Key, e.Status, want.Status))
			}
			if want.Blocking != nil && e.Blocking != *want.Blocking {
				p = append(p, fmt.Sprintf("entry %q: blocking %v, want %v", e.Key, e.Blocking, *want.Blocking))
			}
			if want.Code != "" && e.Code != want.Code {
				p = append(p, fmt.Sprintf("entry %q: code %q, want %q", e.Key, e.Code, want.Code))
			}
			if want.Secret != nil && e.Secret != *want.Secret {
				p = append(p, fmt.Sprintf("entry %q: secret %v, want %v", e.Key, e.Secret, *want.Secret))
			}
		}
		if !found {
			p = append(p, fmt.Sprintf("expected an entry for %q", want.Key))
		}
	}
	return p
}

type pipelineFile struct {
	Version   string `yaml:"version"`
	Pipelines []struct {
		ID          string `yaml:"id"`
		Status      string `yaml:"status"`
		Description string `yaml:"description"`
		Connectors  []struct {
			Plugin string `yaml:"plugin"`
		} `yaml:"connectors"`
	} `yaml:"pipelines"`
}

// checkPipelines checks that each connector's pipeline file exists, has the
// status the report gives it and the provenance description, and collects
// the plugins it references.
func checkPipelines(rep Report, files map[string][]byte, m Manifest) ([]string, []string) {
	var plugins, p []string
	referenced := map[string]bool{}
	for _, con := range rep.Connectors {
		if con.Pipeline == "" {
			continue
		}
		referenced[con.Pipeline] = true
		b, ok := files[con.Pipeline]
		if !ok {
			p = append(p, fmt.Sprintf("%s: report names pipeline %s, which was not written", con.Name, con.Pipeline))
			continue
		}
		var pf pipelineFile
		if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&pf); err != nil {
			p = append(p, fmt.Sprintf("%s: %s is not pipeline YAML: %v", con.Name, con.Pipeline, err))
			continue
		}
		if len(pf.Pipelines) != 1 {
			p = append(p, fmt.Sprintf("%s: %s has %d pipelines, want 1 (one per connector)", con.Name, con.Pipeline, len(pf.Pipelines)))
			continue
		}
		pl := pf.Pipelines[0]
		if pl.Status != con.PipelineStatus {
			p = append(p, fmt.Sprintf("%s: pipeline status %q, report says %q", con.Name, pl.Status, con.PipelineStatus))
		}
		if !strings.Contains(pl.Description, "Migrated from Kafka Connect") || !strings.Contains(pl.Description, "sha256:") {
			p = append(p, fmt.Sprintf("%s: pipeline description lacks the provenance line", con.Name))
		}
		for _, c := range pl.Connectors {
			plugins = append(plugins, c.Plugin)
		}
	}
	n := 0
	for name := range files {
		if strings.HasPrefix(name, pipelinesPrefix) {
			n++
			if !referenced[name] {
				p = append(p, fmt.Sprintf("pipeline file %s is not named by any report connector", name))
			}
		}
	}
	if m.Expect.Pipelines != nil && n != *m.Expect.Pipelines {
		p = append(p, fmt.Sprintf("%d pipeline files written, want %d", n, *m.Expect.Pipelines))
	}
	return plugins, p
}

// checkReportFile requires report.json to hold the same report as the
// envelope's result.
func checkReportFile(result []byte, files map[string][]byte) []string {
	b, ok := files[reportFile]
	if !ok {
		return []string{"report.json was not written"}
	}
	var a, f any
	if json.Unmarshal(result, &a) != nil || json.Unmarshal(b, &f) != nil {
		return []string{"report.json is not JSON"}
	}
	ja, _ := json.Marshal(a)
	jf, _ := json.Marshal(f)
	if !bytes.Equal(ja, jf) {
		return []string{"report.json differs from the --json result"}
	}
	return nil
}

func countPipelines(files map[string][]byte) int {
	n := 0
	for name := range files {
		if strings.HasPrefix(name, pipelinesPrefix) {
			n++
		}
	}
	return n
}

// scanLeaks fails on any secret canary or host path in any output byte,
// stderr included.
func scanLeaks(out Output, hostPaths []string) []string {
	var p []string
	blobs := map[string][]byte{"stdout": out.Stdout, "stderr": out.Stderr}
	for name, b := range out.Files {
		blobs[name] = b
	}
	names := make([]string, 0, len(blobs))
	for n := range blobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := blobs[n]
		if bytes.Contains(b, []byte(SecretCanaryPrefix)) {
			p = append(p, fmt.Sprintf("%s contains a %s* canary: a secret from the input reached the output", n, SecretCanaryPrefix))
		}
		if n == "stderr" {
			continue
		}
		for _, hp := range hostPaths {
			if hp != "" && bytes.Contains(b, []byte(hp)) {
				p = append(p, fmt.Sprintf("%s contains the host path %s (output must not depend on where it ran)", n, hp))
			}
		}
	}
	return p
}
