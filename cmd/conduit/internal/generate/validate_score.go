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
	"os"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// validateCandidate runs the exact engine `conduit pipelines validate` uses
// (validate.Run) against a candidate pipeline-config YAML document held only
// in memory.
//
// validate.Run/RunWithOptions take a file path and read the config from
// disk — there is no in-memory (RunBytes/RunReader) entry point today (see
// docs/design-documents/20260722-conduit-generate.md, Decision §3, "the
// disk seam"). That design doc names the in-memory seam as the PREFERRED
// resolution and a temp-file+cleanup shim as the fallback if the seam
// proves infeasible at generate's build time. This eval harness needs an
// answer now, before that command is built, so it takes the fallback:
// write the candidate to a private (0600) temp file, run validate.Run
// against it, and remove the file before returning — win or lose, on every
// return path (defer, not a final explicit call, so a panic mid-Run still
// cleans up).
//
// This is deliberately the ONLY place in this package that touches disk.
// If/when validate gains RunBytes, this function's body is the only thing
// that needs to change — every caller (ScoreRun) is unaffected.
func validateCandidate(ctx context.Context, candidate string) (validate.Report, error) {
	f, err := os.CreateTemp("", "conduit-generate-eval-*.yaml")
	if err != nil {
		return validate.Report{}, cerrors.Errorf("creating temp file for candidate validation: %w", err)
	}
	path := f.Name()
	// Best-effort cleanup of a private scratch file; nothing downstream
	// depends on the removal succeeding (a leaked temp file in the OS temp
	// dir is a cleanup nuisance, never a correctness or security issue,
	// since it was never a user-visible candidate — see doc.go's "the ONLY
	// place in this package that touches disk").
	defer func() { _ = os.Remove(path) }()

	if _, err := f.WriteString(candidate); err != nil {
		_ = f.Close()
		return validate.Report{}, cerrors.Errorf("writing candidate to temp file %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return validate.Report{}, cerrors.Errorf("closing temp file %q: %w", path, err)
	}

	return validate.Run(ctx, path)
}
