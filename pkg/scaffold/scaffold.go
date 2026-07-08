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

package scaffold

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/scaffold/steps"
	"github.com/conduitio/conduit/pkg/scaffold/template"
)

// Generate scaffolds a connector or processor repository per req: validate
// input, run the toolchain preflight, stage a full write into a temp
// directory, and only rename it into place at req.Path once every step has
// succeeded. A non-nil error is always a *conduiterr.ConduitError with one
// of this package's registered Codes (see codes.go) — Generate never
// returns a bare error.
//
// # Atomicity / no-partial-directory
//
// Every write goes to a hidden temp directory created as a sibling of
// req.Path (same parent directory, so the final os.Rename is same-filesystem
// and near-instant, not a cross-device copy) — never to req.Path directly.
// On any hard failure, the temp directory is removed before Generate
// returns, so req.Path never exists in a half-written state: it is either
// absent (nothing attempted, or a failure before rename) or a complete,
// verified-building scaffold (success). The one exception is
// req.Force-driven removal of a pre-existing req.Path, which happens only
// immediately before the final rename, after the staged tree has already
// passed the build step — see finalize.
func Generate(ctx context.Context, req Request) (Result, error) {
	start := time.Now()

	req, err := validate(req)
	if err != nil {
		return Result{}, err
	}

	var stepResults []StepResult
	run := func(name string, fn func() error) error {
		s := time.Now()
		err := fn()
		stepResults = append(stepResults, StepResult{
			Name:       name,
			OK:         err == nil,
			DurationMS: time.Since(s).Milliseconds(),
			Message:    errMessage(err),
		})
		return err
	}

	// Recorded as its own step (matching the design doc's "✓ Toolchain OK"
	// progress line) even though a failure here returns immediately, before
	// any write — see Generate's doc.
	if err := run(StepToolchain, func() error {
		_, err := preflight(ctx)
		return err
	}); err != nil {
		return Result{}, err
	}

	parent := filepath.Dir(req.Path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, conduiterr.Wrap(CodeWriteFailed, fmt.Sprintf("creating parent directory %q", parent), err)
	}

	stagingDir, err := os.MkdirTemp(parent, ".conduit-scaffold-*")
	if err != nil {
		return Result{}, conduiterr.Wrap(CodeWriteFailed, "creating staging directory", err)
	}
	// Removed on any hard failure below; renamed away (not removed) on
	// success — see the rename at the end of this function.
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	if err := run(StepExtractTemplate, func() error {
		return template.Extract(req.Kind, stagingDir)
	}); err != nil {
		return Result{}, conduiterr.Wrap(CodeWriteFailed, "extracting template snapshot", err)
	}

	if err := run(StepRewriteModule, func() error {
		return template.Rewrite(stagingDir, req.Module, req.Name, req.Kind)
	}); err != nil {
		return Result{}, conduiterr.Wrap(CodeWriteFailed, "rewriting module path and name", err)
	}

	if req.SDKVersion != "" {
		if err := run(StepSDKVersion, func() error {
			return applySDKVersion(ctx, stagingDir, req.Kind, req.SDKVersion)
		}); err != nil {
			return Result{}, conduiterr.Wrap(CodeBuildFailed, fmt.Sprintf("setting SDK version %q", req.SDKVersion), err)
		}
	}

	if !req.SkipGenerate {
		if err := run(StepInstallTools, func() error {
			return steps.InstallTool(ctx, stagingDir, req.Kind)
		}); err != nil {
			return Result{}, conduiterr.Wrap(CodeGenerateFailed, "installing code-gen tool", err)
		}

		if err := run(StepGenerate, func() error {
			return steps.Generate(ctx, stagingDir, req.Kind)
		}); err != nil {
			return Result{}, conduiterr.Wrap(CodeGenerateFailed, "running code generation", err)
		}
	}

	if err := run(StepBuild, func() error {
		return steps.Build(ctx, stagingDir)
	}); err != nil {
		return Result{}, conduiterr.Wrap(CodeBuildFailed, "go build ./... failed", err)
	}

	if req.Git {
		// Best-effort: GitInit's failure does not fail the command (see its
		// doc) — a buildable scaffold with no git history is still usable,
		// and failing the whole run here would undo a successful build over
		// something cosmetic.
		_ = run(StepGitInit, func() error {
			return steps.GitInit(ctx, stagingDir, fmt.Sprintf("chore: scaffold %s %s from conduit %s new", req.Kind, req.Name, req.Kind))
		})
	}

	if err := finalize(stagingDir, req); err != nil {
		return Result{}, conduiterr.Wrap(CodeWriteFailed, "moving scaffold into place", err)
	}
	succeeded = true

	return Result{
		Kind:        req.Kind,
		Language:    req.Language,
		Name:        req.Name,
		Module:      req.Module,
		Path:        req.Path,
		TemplateRef: req.Kind.Ref(),
		SDKVersion:  resolvedSDKVersion(req),
		Steps:       stepResults,
		ElapsedMS:   time.Since(start).Milliseconds(),
		NextSteps:   nextSteps(req),
	}, nil
}

// finalize moves stagingDir into req.Path. When req.Force is set and
// req.Path already exists, the existing directory is removed first — only
// here, after the staged tree has already passed every prior step
// (including the verified build), so a --force run that fails earlier never
// touches the caller's existing directory. See Generate's doc for why this
// ordering matters and what "atomic" does and does not mean here.
func finalize(stagingDir string, req Request) error {
	if req.Force {
		if _, err := os.Stat(req.Path); err == nil {
			if err := os.RemoveAll(req.Path); err != nil {
				return cerrors.Errorf("removing existing %q for --force: %w", req.Path, err)
			}
		}
	}
	if err := os.Rename(stagingDir, req.Path); err != nil {
		// Note: if --force's os.RemoveAll above succeeded but this rename
		// then fails (e.g. disk full, a permissions change racing this
		// call), req.Path is left absent rather than reverted to its prior
		// contents — the same residual risk an ordinary `rm -rf old && mv
		// new old` would have. Generate's caller sees this as a
		// CodeWriteFailed (exit 1) error either way, but it is not a
		// silent clobber: the error is returned, nothing is reported as
		// success.
		return cerrors.Errorf("renaming staging directory into %q: %w", req.Path, err)
	}
	return nil
}

func resolvedSDKVersion(req Request) string {
	if req.SDKVersion != "" {
		return req.SDKVersion
	}
	return req.Kind.SDKVersion()
}

func nextSteps(req Request) []string {
	buildTarget := "make build    # standalone plugin binary"
	if req.Kind == KindProcessor {
		buildTarget = "make build    # standalone WASM plugin"
	}
	return []string{
		"cd " + req.Path,
		"make test     # unit + SDK acceptance suite",
		buildTarget,
		"conduit run   # wired into a pipeline",
	}
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
