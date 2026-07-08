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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/scaffold/template"
)

// Generate runs kind's code generation against dir, after InstallTool has
// put the right binary on PATH. Both templates' Makefile `generate` target
// starts with `go generate ./...`, which is enough on its own for a
// processor (its only //go:generate directive is paramgen). A connector's
// Makefile target has a second line, `conn-sdk-cli readmegen -w`, which
// regenerates the README's parameter tables from connector.yaml — Generate
// runs that too, only for KindConnector. This is intentionally not
// `exec.Command("make", "generate")`: shelling out to make would work
// today, but it would make this package's behavior implicit (whatever the
// Makefile happens to say) instead of explicit and reviewable, and it adds
// an undeclared `make`-on-PATH requirement this command's preflight does not
// check for (see scaffold.preflightChecks, which checks only go and git).
func Generate(ctx context.Context, dir string, kind template.Kind) error {
	if _, err := runGo(ctx, dir, "generate", "./..."); err != nil {
		return cerrors.Errorf("steps: go generate: %w", err)
	}

	if kind == template.KindConnector {
		if err := runReadmegen(ctx, dir); err != nil {
			return err
		}
	}

	return nil
}

func runReadmegen(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "conn-sdk-cli", "readmegen", "-w")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cerrors.Errorf("steps: conn-sdk-cli readmegen -w: %w: %s", err, string(out))
	}
	return nil
}
