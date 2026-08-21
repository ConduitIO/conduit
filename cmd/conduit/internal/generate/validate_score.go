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
func validateCandidate(ctx context.Context, candidate string) validate.Report {
	return validate.RunBytes(ctx, InMemoryCandidateName, []byte(candidate), candidateValidateOptions())
}
