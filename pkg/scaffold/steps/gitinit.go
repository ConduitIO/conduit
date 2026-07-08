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

package steps

import (
	"context"
	"os/exec"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// fallbackGitIdentity is used for the initial scaffold commit's author only
// when the local repository has no user.name/user.email configured (neither
// via --global nor an environment default) — otherwise `git commit` fails
// outright with "Author identity unknown", which would make git
// initialization fail in exactly the environments most likely to run this
// command non-interactively (CI, a fresh container with no git config).
// When the caller's environment already has git identity configured, that
// identity is used instead (see GitInit): this is a fallback, not an
// override.
const (
	fallbackGitName  = "Conduit Scaffold"
	fallbackGitEmail = "scaffold@conduit.io"
)

// GitInit initializes a git repository in dir and creates a first commit
// containing every file the scaffold wrote. message is the commit message
// (the caller supplies one that names the connector/processor and the
// command that created it).
//
// GitInit's errors are not classified as fatal by scaffold.Generate — see
// that package's doc: a scaffold that builds but has no git history is
// still a usable result, so a git failure (e.g. git itself was on PATH for
// the preflight check but broken in some other way) is surfaced as a failed
// step, not a hard command failure.
func GitInit(ctx context.Context, dir, message string) error {
	if _, err := runGit(ctx, dir, "init"); err != nil {
		return cerrors.Errorf("steps: git init: %w", err)
	}
	if _, err := runGit(ctx, dir, "add", "-A"); err != nil {
		return cerrors.Errorf("steps: git add -A: %w", err)
	}

	commitArgs := []string{"commit", "-m", message}
	if name, _ := runGit(ctx, dir, "config", "user.name"); strings.TrimSpace(name) == "" {
		commitArgs = append([]string{"-c", "user.name=" + fallbackGitName}, commitArgs...)
	}
	if email, _ := runGit(ctx, dir, "config", "user.email"); strings.TrimSpace(email) == "" {
		commitArgs = append([]string{"-c", "user.email=" + fallbackGitEmail}, commitArgs...)
	}

	if _, err := runGit(ctx, dir, commitArgs...); err != nil {
		return cerrors.Errorf("steps: git commit: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), cerrors.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
