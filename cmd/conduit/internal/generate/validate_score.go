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

package generate

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
)

// validateCandidate runs the exact gate `conduit generate` itself runs
// against a candidate pipeline-config YAML document held only in memory:
// validate.RunBytes with candidateValidateOptions() (ResolvePlugins on, the
// builtin connector/processor sets — see generate.go's doc comment on
// candidateValidateOptions for why that option is not the zero value).
//
// This used to differ from the shipped command: it called validate.Run
// (Options{}, ResolvePlugins off) against a private temp file, because
// RunBytes did not exist yet (see docs/design-documents/20260722-conduit-
// generate.md, Decision §3, "the disk seam", and this function's own prior
// doc comment naming RunBytes as the trigger to change). That gap meant a
// candidate referencing a fabricated builtin plugin (e.g.
// `builtin:postgre`) scored validate-PASS in this harness while the real
// command would have scored it validate-FAIL — the harness was measuring a
// strictly weaker gate than the one it exists to benchmark, in exactly the
// failure class (a plausible-looking fabricated plugin name) this feature
// must never ship to a user.
//
// Now that RunBytes exists (cmd/conduit/internal/validate/engine.go:129),
// this function is the ONLY thing that needed to change, exactly as
// promised — no disk write, no temp file, no caller (ScoreRun) touched.
//
// An empty or whitespace-only candidate is special-cased rather than
// handed to validate.RunBytes (B2, round-3 review of #2814):
// validate.RunBytes on zero bytes reports zero findings — there is nothing
// in an empty document to find a problem WITH — so Report.OK() would read
// true, scoring a validate PASS for a request the model never produced
// anything usable for. That happens for real: runCapture
// (transcript_capture_test.go) stores gen.Candidate verbatim in its
// Candidates map even when every attempt failed extraction (raw completions
// WERE recorded — this is DATA, never treated as an absent request — but
// none of them parsed into pipeline YAML), which is exactly what an empty
// string here means. ScoreRun (score.go) ALSO guards this directly and is
// the authoritative layer for the harness's own numbers — it is the one
// place both the validate AND semantic axes are scored for a request, and
// only it can fail SemanticMatch too, which this function has no way to
// reach. This is the second, narrower layer, kept in sync so any OTHER
// present or future caller of validateCandidate gets a correct answer too.
func validateCandidate(ctx context.Context, candidate string) validate.Report {
	if strings.TrimSpace(candidate) == "" {
		return validate.Report{
			Summary: validate.Summary{Files: 1, Errors: 1},
			Files: []validate.FileReport{{
				Path: InMemoryCandidateName,
				OK:   false,
				Findings: []validate.Finding{{
					Severity: validate.SeverityError,
					Code:     "generate.empty_candidate",
					Message:  "the candidate is empty or whitespace-only — nothing was generated to validate",
				}},
			}},
		}
	}
	return validate.RunBytes(ctx, InMemoryCandidateName, []byte(candidate), candidateValidateOptions())
}
