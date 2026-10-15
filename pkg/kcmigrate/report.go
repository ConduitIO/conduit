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

package kcmigrate

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// ReportVersion is the version of the report contract this package writes.
// It bumps only when a field is removed or renamed, or when the meaning of
// status or blocking changes; additions keep it. Consumers must ignore
// unknown fields.
const ReportVersion = 1

// Status is how a key was handled.
type Status string

const (
	// StatusTranslated means the Conduit configuration is behaviorally
	// equivalent for the key's documented semantics, including keys that are
	// no-ops by design (the reason says so).
	StatusTranslated Status = "translated"
	// StatusApproximated means Conduit does something close and the reason
	// states the difference.
	StatusApproximated Status = "approximated"
	// StatusUnsupported means there is no Conduit equivalent in this release;
	// fix says what to do instead.
	StatusUnsupported Status = "unsupported"
)

// Role is a connector's role in the report.
type Role string

const (
	RoleSource  Role = "source"
	RoleSink    Role = "sink"
	RoleWorker  Role = "worker"  // a worker properties file, one element per file
	RoleUnknown Role = "unknown" // the class is not recognized
)

// PipelineStatus is the status a generated pipeline is written with.
type PipelineStatus string

const (
	PipelineRunning PipelineStatus = "running"
	PipelineStopped PipelineStatus = "stopped"
)

// Report is the compatibility report: report.json, and the `result` of the
// --json envelope, whose `summary` is Report.Summary. The JSON form is the
// public contract in schema/report.v1.schema.json; Validate checks the rules
// the schema expresses plus the ones it cannot (the summary partition).
type Report struct {
	ReportVersion int               `json:"reportVersion"`
	Generator     Generator         `json:"generator"`
	Summary       Summary           `json:"summary"`
	Connectors    []ReportConnector `json:"connectors"`
	// AcceptedGaps lists the blocking entries and notes that --accept-gaps
	// let through. Empty without that flag.
	AcceptedGaps []AcceptedGap `json:"acceptedGaps"`
}

// Generator records what produced the report, so a bug report can be
// reproduced from the report alone.
type Generator struct {
	Conduit string      `json:"conduit"`
	Rules   []RulesInfo `json:"rules"`
}

// RulesInfo identifies one rule file the run used.
type RulesInfo struct {
	Class        string   `json:"class"`
	RulesVersion int      `json:"rulesVersion"`
	Upstream     Upstream `json:"upstream"`
}

// Upstream is the upstream project and version a rule file was written
// against, for example debezium 3.7.0.Final.
type Upstream struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Summary counts entries. Keys is the number of entries, which equals the
// number of input key occurrences; Translated, Approximated and Unsupported
// partition it. Blocking counts blocking entries and notes.
type Summary struct {
	Connectors   int `json:"connectors"`
	Keys         int `json:"keys"`
	Translated   int `json:"translated"`
	Approximated int `json:"approximated"`
	Unsupported  int `json:"unsupported"`
	Blocking     int `json:"blocking"`
}

// ReportConnector is one connector (or worker file) in the report.
type ReportConnector struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	Role  Role   `json:"role"`
	// Pipeline is the generated file's path relative to the output directory,
	// empty when no pipeline is written (unknown class, worker file).
	Pipeline       string         `json:"pipeline,omitempty"`
	PipelineStatus PipelineStatus `json:"pipelineStatus,omitempty"`
	Target         *Target        `json:"target,omitempty"`
	// Entries has one element per input key occurrence, in origin order.
	Entries []Entry `json:"entries"`
	// Notes are differences with no input key to attach to (absent-key
	// defaults, output shape, delivery semantics, required settings). They
	// are outside the completeness multiset.
	Notes []Note `json:"notes"`
	// Transforms is an index of the SMT chain; every SMT sub-key still has
	// its own entry in Entries.
	Transforms []Transform `json:"transforms"`
	// Resume describes how the pipeline starts relative to the original
	// connector's position (the Postgres offset import, #3029).
	Resume *Resume `json:"resume,omitempty"`
	// OutputCompat describes Debezium-compatible output mode (#3033) when the
	// pipeline uses it.
	OutputCompat *OutputCompat `json:"outputCompat,omitempty"`
}

// Target names the Conduit plugins a connector maps to and their
// certification tier at run time.
type Target struct {
	Source        string            `json:"source,omitempty"`
	Destination   string            `json:"destination,omitempty"`
	Certification map[string]string `json:"certification,omitempty"`
}

// Entry is the outcome for one input key occurrence.
type Entry struct {
	Key      string       `json:"key"`
	Status   Status       `json:"status"`
	Origin   Origin       `json:"origin"`
	Target   *EntryTarget `json:"target,omitempty"`
	Code     string       `json:"code,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	Fix      string       `json:"fix,omitempty"`
	Blocking bool         `json:"blocking"`
	// Secret means the value was replaced by an environment variable
	// reference (Target.Env) and appears in no output.
	Secret bool `json:"secret,omitempty"`
}

// EntryTarget locates what a key became in the generated pipeline.
type EntryTarget struct {
	// ConfigPath is a JSON pointer into the generated pipeline file.
	ConfigPath string `json:"configPath,omitempty"`
	// Env is the environment variable a secret was replaced with.
	Env string `json:"env,omitempty"`
}

// Note is a connector-level difference with no input key.
type Note struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	Fix      string `json:"fix,omitempty"`
	Blocking bool   `json:"blocking"`
	// Rule names the rule that emitted the note, for corpus expectations.
	Rule string `json:"rule"`
}

// Transform is one SMT of a connector's chain.
type Transform struct {
	Alias     string `json:"alias"`
	Class     string `json:"class,omitempty"`
	Status    Status `json:"status"`
	Processor string `json:"processor,omitempty"`
}

// Resume follows the base design's example: strategy "fresh" or "reuse",
// snapshot "will-run" or "skipped", the offset import locator when one is
// used, and the state of the connector preflights, which run online and are
// "pending" in an offline report.
type Resume struct {
	Strategy   string        `json:"strategy"`
	Snapshot   string        `json:"snapshot"`
	Import     *ResumeImport `json:"import,omitempty"`
	Preflights string        `json:"preflights,omitempty"`
	Notes      []string      `json:"notes,omitempty"`
}

// ResumeImport locates the imported Debezium offset. The offset is not a
// secret.
type ResumeImport struct {
	Setting string `json:"setting"`
	Source  string `json:"source"`
	Topic   string `json:"topic,omitempty"`
	Key     string `json:"key,omitempty"`
}

// OutputCompat is the #3033 report field: mode, compat version, converters,
// subject strategy and the ids of the intentional differences that apply.
type OutputCompat struct {
	Mode            string     `json:"mode"`
	Compat          string     `json:"compat,omitempty"`
	Converters      Converters `json:"converters"`
	SubjectStrategy string     `json:"subjectStrategy,omitempty"`
	Differences     []string   `json:"differences"`
}

// Converters names the converter classes of a connector.
type Converters struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
	Header string `json:"header,omitempty"`
}

// AcceptedGap is a blocking entry (Key and Origin set) or note (Key empty)
// that --accept-gaps let through.
type AcceptedGap struct {
	Connector string  `json:"connector"`
	Code      string  `json:"code"`
	Key       string  `json:"key,omitempty"`
	Origin    *Origin `json:"origin,omitempty"`
}

// NewReport returns an empty report with the contract version set and every
// list non-nil, so the JSON form always has arrays, never nulls.
func NewReport(conduitVersion string) *Report {
	return &Report{
		ReportVersion: ReportVersion,
		Generator:     Generator{Conduit: conduitVersion, Rules: []RulesInfo{}},
		Connectors:    []ReportConnector{},
		AcceptedGaps:  []AcceptedGap{},
	}
}

// Summarize recomputes Summary from the connectors.
func (r *Report) Summarize() {
	s := Summary{Connectors: len(r.Connectors)}
	for _, c := range r.Connectors {
		for _, e := range c.Entries {
			s.Keys++
			switch e.Status {
			case StatusTranslated:
				s.Translated++
			case StatusApproximated:
				s.Approximated++
			case StatusUnsupported:
				s.Unsupported++
			}
			if e.Blocking {
				s.Blocking++
			}
		}
		for _, n := range c.Notes {
			if n.Blocking {
				s.Blocking++
			}
		}
	}
	r.Summary = s
}

// OK reports the run's verdict: no blocking entry or note.
func (r *Report) OK() bool { return r.Summary.Blocking == 0 }

// Validate checks the report against the contract rules that every producer
// must uphold: a known status on every entry, a reason on every approximated
// and unsupported entry, a code on every entry that is not translated, a fix
// on every unsupported or blocking entry and every blocking note, and a
// Summary equal to what Summarize computes. A violation is a translator bug.
func (r *Report) Validate() error {
	var errs []error
	for _, c := range r.Connectors {
		if (c.Pipeline == "") != (c.PipelineStatus == "") {
			errs = append(errs, cerrors.Errorf("connector %q: pipeline and pipelineStatus must be set together", c.Name))
		}
		for _, e := range c.Entries {
			at := fmt.Sprintf("connector %q key %q at %s", c.Name, e.Key, e.Origin)
			switch e.Status {
			case StatusTranslated:
			case StatusApproximated, StatusUnsupported:
				if e.Reason == "" {
					errs = append(errs, cerrors.Errorf("%s: %s entry has no reason", at, e.Status))
				}
				if e.Code == "" {
					errs = append(errs, cerrors.Errorf("%s: %s entry has no code", at, e.Status))
				}
			default:
				errs = append(errs, cerrors.Errorf("%s: unknown status %q", at, e.Status))
			}
			if (e.Status == StatusUnsupported || e.Blocking) && e.Fix == "" {
				errs = append(errs, cerrors.Errorf("%s: unsupported or blocking entry has no fix", at))
			}
		}
		for _, n := range c.Notes {
			if n.Code == "" || n.Reason == "" || n.Rule == "" {
				errs = append(errs, cerrors.Errorf("connector %q: note %q lacks code, reason or rule", c.Name, n.Code))
			}
			if n.Blocking && n.Fix == "" {
				errs = append(errs, cerrors.Errorf("connector %q: blocking note %q has no fix", c.Name, n.Code))
			}
		}
	}
	want := *r
	want.Summarize()
	if want.Summary != r.Summary {
		errs = append(errs, cerrors.Errorf("summary %+v does not match the entries (%+v)", r.Summary, want.Summary))
	}
	return cerrors.Join(errs...)
}

// MarshalReport returns the canonical JSON form: two-space indented, fields
// in declaration order, map keys sorted, no HTML escaping, nil lists written
// as [], a trailing newline. The pipeline description's report hash is
// computed over these bytes, so they must be deterministic.
func MarshalReport(r *Report) ([]byte, error) {
	out := *r
	out.Generator.Rules = nonNil(out.Generator.Rules)
	out.AcceptedGaps = nonNil(out.AcceptedGaps)
	out.Connectors = make([]ReportConnector, len(r.Connectors))
	for i, c := range r.Connectors {
		c.Entries, c.Notes, c.Transforms = nonNil(c.Entries), nonNil(c.Notes), nonNil(c.Transforms)
		if c.OutputCompat != nil {
			oc := *c.OutputCompat
			oc.Differences = nonNil(oc.Differences)
			c.OutputCompat = &oc
		}
		out.Connectors[i] = c
	}
	b, err := json.MarshalIndentWithOption(&out, "", "  ", json.DisableHTMLEscape())
	if err != nil {
		return nil, cerrors.Errorf("marshal report: %w", err)
	}
	return append(b, '\n'), nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Origin locates a key in the input. Properties inputs set Line and Column
// (1-based, of the key's first character); JSON inputs set Pointer (RFC 6901).
// An Origin never carries a value.
type Origin struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Pointer string `json:"pointer,omitempty"`
}

// String renders the origin the way findings print it: "file:line:col" or
// "file#pointer".
func (o Origin) String() string {
	if o.Pointer != "" {
		return o.File + "#" + o.Pointer
	}
	return fmt.Sprintf("%s:%d:%d", o.File, o.Line, o.Column)
}
