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

package validate

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// Severity is a Finding's severity. `validate` only ever produces
// SeverityError findings (it is errors-only, per the CLI output
// conventions' `--strict` row: validate has no `--strict` flag because it
// has nothing for `--strict` to escalate). SeverityWarning exists so the
// same Finding shape carries `lint`'s advisory findings without a second
// type, once that verb ships.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Finding is one located problem in a pipeline config file: the shared
// "located finding" shape from the CLI output conventions (§1.1), reused
// verbatim so `pipelines validate` and a future `doctor`/`lint` render and
// marshal identically.
type Finding struct {
	Severity   Severity        `json:"severity"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	ConfigPath string          `json:"configPath,omitempty"`
	Suggestion string          `json:"suggestion,omitempty"`
	Fix        *conduiterr.Fix `json:"fix,omitempty"`
	// Line and Column locate an advisory `lint` warning in the source file
	// (1-based; 0 = unknown). They are only ever set on SeverityWarning
	// findings surfaced by the parser — error findings locate via ConfigPath.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// FileReport is one resolved file's outcome: every pipeline ID found in it
// (enriched, i.e. after config.Enrich — connector/processor IDs are already
// pipelineID-prefixed by the time a caller sees this) and every Finding
// collected while parsing, enriching, and validating it. OK is true iff
// Findings is empty.
type FileReport struct {
	Path      string    `json:"path"`
	OK        bool      `json:"ok"`
	Pipelines []string  `json:"pipelines"`
	Findings  []Finding `json:"findings"`
	// Enriched is the fully-enriched configuration (final connector/processor
	// IDs, injected DLQ defaults, worker counts) of each pipeline in this file.
	// It is only populated by `pipelines dry-run` (Options.Enriched); validate
	// and lint leave it nil so their --json output is unchanged.
	Enriched []config.Pipeline `json:"enriched,omitempty"`
}

// Summary is the report-wide rollup `pipelines validate` renders as its
// "Summary: ..." line and emits as the --json envelope's `summary` field.
type Summary struct {
	Files     int `json:"files"`
	Pipelines int `json:"pipelines"`
	Errors    int `json:"errors"`
	Warnings  int `json:"warnings"`
}

// Result is the --json envelope's `result` payload for `pipelines
// validate`: every resolved file's report, in the same (sorted-by-path)
// order Run produced them in.
type Result struct {
	Files []FileReport `json:"files"`
}

// Report is Run's return value: every file's outcome plus the rollup
// Summary. OK reports the run-wide verdict (CLI output conventions §1's
// envelope `ok` field) — true iff no file has any error-severity finding.
type Report struct {
	Summary Summary
	Files   []FileReport
}

// OK reports whether every resolved file passed (no error-severity
// findings). A directory with zero matching files is OK — per the design
// doc's failure modes, an empty directory is "0 pipelines checked", not an
// error.
func (r Report) OK() bool { return r.Summary.Errors == 0 }
