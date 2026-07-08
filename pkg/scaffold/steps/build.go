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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Build runs `go build ./...` in dir and returns an error (with the
// compiler's own output attached) if it fails. This is the command's
// central promise — a scaffolded connector/processor "builds with no
// edits" — and is run unconditionally, even when --skip-generate was given:
// both templates ship their generated output pre-committed (connector.yaml,
// paramgen_proc.go), so a build is meaningful with or without a fresh
// generate run.
//
// This intentionally does not build cmd/connector or cmd/processor's
// standalone plugin binary (`make build`, which for a processor
// cross-compiles to GOOS=wasip1 GOARCH=wasm) — that deeper verification
// (the binary actually loads and runs in a pipeline) is the follow-up <30m
// CI matrix job's job, per the design doc's acceptance criteria #3/#5; this
// function only proves the package graph compiles for the host toolchain.
func Build(ctx context.Context, dir string) error {
	if _, err := runGo(ctx, dir, "build", "./..."); err != nil {
		return cerrors.Errorf("steps: go build ./...: %w", err)
	}
	return nil
}
