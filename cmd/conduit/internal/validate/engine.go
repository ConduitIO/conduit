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
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// Run resolves path (a single .yml/.yaml file or a directory of them) and
// runs the offline parse -> enrich -> validate pipeline over every file it
// finds, collecting every finding — parse failures, field-validation
// errors, and cross-file/-document duplicate pipeline IDs — across every
// file. It never fails fast: one bad file never stops the others from being
// checked.
//
// Run returns a non-nil error only when path itself can't be resolved at
// all (e.g. it does not exist) — that is a hard failure the caller (the
// pipelines validate command) should render as a top-level command error,
// not a Finding, since there is no file to attach it to. Everything else —
// including every file being unparseable — comes back as findings inside a
// (possibly all-failing) Report, with a nil error.
func Run(ctx context.Context, path string) (Report, error) {
	return RunWithOptions(ctx, path, Options{})
}

// Options selects which additional, opt-in checks the engine runs on top of
// the always-on parse -> enrich -> validate. The zero value is exactly
// `pipelines validate` (errors only, offline). `pipelines lint` sets Warnings;
// `pipelines dry-run` will set ResolvePlugins.
type Options struct {
	// Warnings surfaces the parser's advisory warnings (deprecated/renamed/
	// unknown fields, version fallback) as SeverityWarning findings. `lint`
	// sets this; `validate` does not (it is errors-only).
	Warnings bool
}

// RunWithOptions is Run with the opt-in checks in opts. Run(ctx, path) is
// RunWithOptions(ctx, path, Options{}).
func RunWithOptions(ctx context.Context, path string, opts Options) (Report, error) {
	files, err := config.ResolveFiles(path)
	if err != nil {
		return Report{}, err
	}
	sort.Strings(files) // deterministic order, independent of directory-read order

	frs := make([]fileState, len(files))
	for i, f := range files {
		frs[i] = validateFile(ctx, f, opts)
	}

	checkCrossFileDuplicateIDs(frs)

	return buildReport(frs), nil
}

// CodeLintWarning is the stable code carried by every advisory `lint` warning
// finding. The parser's warnings (deprecated/renamed/unknown fields, version
// fallback) don't carry per-field conduiterr codes, so lint attaches this one
// so a --json consumer can still filter on `code`. It is advisory-only:
// warnings never affect the exit code unless `lint --strict` is set (see
// ExitErrorStrict), so this code never flows through ExitError's bucket
// classification.
const CodeLintWarning = "config.lint_warning"

// fileState is the engine's working representation of one resolved file: the
// enriched pipelines it parsed successfully (used for cross-file duplicate-ID
// detection, which needs every file's pipeline IDs at once) plus the
// findings collected so far. buildReport converts this into the public
// FileReport once every file (and the cross-file pass) has run.
type fileState struct {
	path      string
	pipelines []config.Pipeline
	findings  []Finding
}

// validateFile runs parse -> enrich -> validate (in that order — enrichment
// mutates connector/processor IDs that validation checks, matching
// pkg/provisioning.Service.provisionPipeline's ordering at service.go:279-280)
// over a single file, collecting every finding along the way.
func validateFile(ctx context.Context, path string, opts Options) fileState {
	fs := fileState{path: path}

	f, err := os.Open(path)
	if err != nil {
		ce := conduiterr.Wrap(config.CodeParseError, fmt.Sprintf("could not open file %q: %v", path, err), err)
		ce.Suggestion = "check that the file exists and is readable"
		fs.findings = append(fs.findings, findingFromError(ce))
		return fs
	}
	defer f.Close()

	parser := yaml.NewParser(log.Nop())

	// Both Parse and ParseWithWarnings return the same []config.Pipeline and the
	// same raw cerrors.Join of per-document failures — the only difference is
	// ParseWithWarnings also returns the advisory warnings the run path only
	// logs (see parser.go). lint asks for them; validate does not.
	var pipelines []config.Pipeline
	if opts.Warnings {
		var warns []yaml.Warning
		pipelines, warns, err = parser.ParseWithWarnings(ctx, f)
		for _, w := range warns {
			fs.findings = append(fs.findings, warningFinding(w))
		}
	} else {
		pipelines, err = parser.Parse(ctx, f)
	}
	// err here is the parser's raw cerrors.Join of per-document failures (no
	// extra "%w" wrap) — walk it directly with cerrors.ForEach. Wrapping it
	// first (e.g. via cerrors.Errorf("...: %w", err)) would collapse every
	// finding into one; see doc.go and TestValidateFile_ParseErrors_AllFindings.
	if err != nil {
		cerrors.ForEach(err, func(e error) {
			fs.findings = append(fs.findings, findingFromError(e))
		})
	}

	for _, p := range pipelines {
		enriched := config.Enrich(p)
		fs.pipelines = append(fs.pipelines, enriched)

		// config.Validate also returns a raw cerrors.Join — same rule as
		// above, walk it directly, do not wrap it first.
		if verr := config.Validate(enriched); verr != nil {
			cerrors.ForEach(verr, func(e error) {
				fs.findings = append(fs.findings, findingFromError(e))
			})
		}
	}

	return fs
}

// findingFromError converts one element of a cerrors.ForEach walk into a
// Finding. A *conduiterr.ConduitError (every config.Validate error, and any
// error this package itself wraps with a Code) carries its own code,
// configPath, and suggestion through as-is. A plain error (a YAML syntax or
// unrecognized-version failure from the parser, which has no per-field
// configPath to attach) still gets a stable code so no finding is ever
// uncoded — see design doc AC-5, "unparseable/unknown-version -> exit 2
// coded finding, not panic".
func findingFromError(e error) Finding {
	if ce, ok := conduiterr.Get(e); ok {
		return Finding{
			Severity:   SeverityError,
			Code:       ce.Code.Reason(),
			Message:    ce.Message,
			ConfigPath: ce.ConfigPath,
			Suggestion: ce.Suggestion,
			Fix:        ce.Fix,
		}
	}

	ce := conduiterr.Wrap(config.CodeParseError, e.Error(), e)
	if ce.Suggestion == "" {
		ce.Suggestion = `fix the YAML syntax, or set "version" to a supported pipeline config version`
	}
	return Finding{
		Severity:   SeverityError,
		Code:       ce.Code.Reason(),
		Message:    ce.Message,
		ConfigPath: ce.ConfigPath,
		Suggestion: ce.Suggestion,
		Fix:        ce.Fix,
	}
}

// warningFinding converts one parser Warning into an advisory (SeverityWarning)
// Finding. Warnings locate by line/column in the source file rather than by a
// config-path JSON pointer, and carry the fixed CodeLintWarning code (the
// parser's warnings have no per-field conduiterr code of their own). A warning
// never affects the exit code unless `lint --strict` is set.
func warningFinding(w yaml.Warning) Finding {
	return Finding{
		Severity:   SeverityWarning,
		Code:       CodeLintWarning,
		Message:    w.Message,
		ConfigPath: w.Field,
		Line:       w.Line,
		Column:     w.Column,
	}
}

// checkCrossFileDuplicateIDs is new code, not a reuse of
// pkg/provisioning.Service.findDuplicateIDs: that method returns a bare
// index map and a sentinel error (ErrDuplicatedPipelineID) suited to
// provisioning's "skip the duplicates, keep going" recovery, not a coded,
// located Finding. This walks every file's successfully-parsed (and
// enriched — though enrichment never touches the pipeline's own top-level
// ID, so parsed vs. enriched makes no difference here) pipeline IDs at once
// and appends a provisioning.CodePipelineIDDuplicate Finding, with a
// configPath into that file's own "pipelines" list, to every file that
// contributes an occurrence of a duplicated ID — including two different
// files sharing one ID, which findDuplicateIDs was never asked to locate
// (it only ever saw the flattened, already-merged list).
func checkCrossFileDuplicateIDs(frs []fileState) {
	type loc struct {
		fileIdx, pipeIdx int
	}
	byID := map[string][]loc{}
	for fi, fr := range frs {
		for pi, p := range fr.pipelines {
			if p.ID == "" {
				continue // already reported via config.CodeFieldRequired; don't double up
			}
			byID[p.ID] = append(byID[p.ID], loc{fileIdx: fi, pipeIdx: pi})
		}
	}

	// Sort IDs for deterministic finding order across runs.
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		locs := byID[id]
		if len(locs) < 2 {
			continue
		}
		sort.Slice(locs, func(i, j int) bool {
			if locs[i].fileIdx != locs[j].fileIdx {
				return frs[locs[i].fileIdx].path < frs[locs[j].fileIdx].path
			}
			return locs[i].pipeIdx < locs[j].pipeIdx
		})

		files := make([]string, 0, len(locs))
		for _, l := range locs {
			files = append(files, frs[l.fileIdx].path)
		}

		for _, l := range locs {
			ce := conduiterr.New(provisioning.CodePipelineIDDuplicate,
				fmt.Sprintf("pipeline %q: id is used by %d pipelines (%s)", id, len(locs), strings.Join(files, ", ")))
			ce.ConfigPath = fmt.Sprintf("/pipelines/%d/id", l.pipeIdx)
			ce.Suggestion = fmt.Sprintf("rename this pipeline's id or the other pipeline(s) sharing %q", id)

			frs[l.fileIdx].findings = append(frs[l.fileIdx].findings, Finding{
				Severity:   SeverityError,
				Code:       ce.Code.Reason(),
				Message:    ce.Message,
				ConfigPath: ce.ConfigPath,
				Suggestion: ce.Suggestion,
			})
		}
	}
}

// buildReport converts the engine's working fileState slice into the public
// Report: findings sorted by configPath within each file (the design doc's
// ordering guarantee), plus the report-wide Summary rollup.
func buildReport(frs []fileState) Report {
	report := Report{Files: make([]FileReport, len(frs))}
	report.Summary.Files = len(frs)

	for i, fr := range frs {
		sort.SliceStable(fr.findings, func(a, b int) bool {
			return fr.findings[a].ConfigPath < fr.findings[b].ConfigPath
		})

		ids := make([]string, 0, len(fr.pipelines))
		for _, p := range fr.pipelines {
			ids = append(ids, p.ID)
		}

		findings := fr.findings
		if findings == nil {
			// A nil slice marshals to JSON `null`; a --json consumer
			// iterating result.files[].findings shouldn't have to special-case
			// "no findings" as null vs. an empty array.
			findings = []Finding{}
		}

		report.Files[i] = FileReport{
			Path:      fr.path,
			OK:        len(fr.findings) == 0,
			Pipelines: ids,
			Findings:  findings,
		}

		report.Summary.Pipelines += len(ids)
		for _, find := range fr.findings {
			switch find.Severity {
			case SeverityWarning:
				report.Summary.Warnings++
			case SeverityError:
				report.Summary.Errors++
			default:
				// Every Finding this package constructs sets Severity
				// explicitly (findingFromError and
				// checkCrossFileDuplicateIDs both always use SeverityError;
				// `lint`, in a future PR, will be the first caller to ever
				// produce SeverityWarning). An unset/unknown value is a bug
				// in a finding constructor, not a real severity — counting
				// it as an error is the conservative choice (never silently
				// under-report a problem into exit 0).
				report.Summary.Errors++
			}
		}
	}

	return report
}
