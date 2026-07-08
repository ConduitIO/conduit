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
	"github.com/conduitio/conduit/pkg/scaffold/template"
)

// generatorTool maps a template.Kind to the Go import path of the binary its
// `go:generate` directive needs on PATH — conn-sdk-cli's specgen subcommand
// for a connector (connector.go: "//go:generate conn-sdk-cli specgen"),
// paramgen for a processor (processor.go:
// "//go:generate paramgen -output=paramgen_proc.go ProcessorConfig"). This
// is the crux of the connector/processor asymmetry: the two templates'
// generate steps are driven by different tools, not the same tool with
// different arguments, so there is no way to express this as one constant.
var generatorTool = map[template.Kind]string{
	template.KindConnector: "github.com/conduitio/conduit-connector-sdk/conn-sdk-cli",
	template.KindProcessor: "github.com/conduitio/conduit-commons/paramgen",
}

// InstallTool installs, into GOBIN (or GOPATH/bin), the single binary kind's
// Generate step needs — not every tool declared in the snapshot's
// tools/go.mod (both templates' tools/go.mod also declare golangci-lint and
// gofumpt, needed for `make lint`/`make fmt` but not for producing a
// building repo, which is this command's job; installing them here would
// only add minutes to every scaffold for no verification benefit).
//
// The version installed is read back out of dir's own tools/go.mod (via `go
// list -modfile`), not hardcoded in this package, so a resync of the
// embedded snapshot (package template) automatically carries a version bump
// through to what gets installed — the alternative, pinning a version
// string here too, is a second place to remember to update and will drift.
func InstallTool(ctx context.Context, dir string, kind template.Kind) error {
	pkg, ok := generatorTool[kind]
	if !ok {
		return cerrors.Errorf("steps: no generator tool registered for kind %q", kind)
	}

	version, err := runGo(ctx, dir, "list", "-modfile=tools/go.mod", "-f", "{{.Module.Version}}", pkg)
	if err != nil {
		return cerrors.Errorf("steps: resolving pinned version of %s from tools/go.mod: %w", pkg, err)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return cerrors.Errorf("steps: tools/go.mod does not pin a version for %s", pkg)
	}

	if _, err := runGo(ctx, dir, "install", pkg+"@"+version); err != nil {
		return cerrors.Errorf("steps: installing %s@%s: %w", pkg, version, err)
	}
	return nil
}

// runGo runs `go <args...>` with dir as the working directory and returns
// combined stdout+stderr. Combined output is deliberate: a failing `go
// install` or `go list` typically explains itself on stderr, and folding it
// into the returned error is what makes InstallTool/Generate/Build's errors
// actionable instead of a bare "exit status 1".
func runGo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), cerrors.Errorf("go %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
