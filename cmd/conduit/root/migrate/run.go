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

package migrate

import (
	"context"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Run is the single code path behind both `conduit migrate kafka-connect`
// and the kc_migrate MCP tool (mapping design, "MCP tool parity": "the CLI
// and the tools call kcmigrate.Run"). The two callers differ only in where
// the inputs come from (files vs. content) and where the outputs go (--out
// vs. returned); the migration itself — rule engine, report, files, verdict
// — is migrate.Run, unchanged.
//
// Run fills the request's ConduitVersion, ValidatePipeline and
// RequiredSettings when the caller left them zero, so a migration is never
// run without the pipeline validator or the plugins' required settings —
// both are required by migrate.Run: a generated pipeline that does not
// validate, or that lacks a required setting, is a translator bug, never a
// result handed to the user. A caller that sets one of them (a test) keeps
// its override.
func Run(ctx context.Context, req migrate.Request) (migrate.Result, error) {
	engine, err := rules.Builtin()
	if err != nil {
		return migrate.Result{}, conduiterr.Wrap(conduiterr.CodeInternal, "the embedded migration rules are invalid", err)
	}
	if req.ConduitVersion == "" {
		req.ConduitVersion = conduit.Version(false)
	}
	if req.ValidatePipeline == nil {
		req.ValidatePipeline = ValidatePipeline(ctx)
	}
	if req.RequiredSettings == nil {
		req.RequiredSettings = migrate.BuiltinRequired
	}
	return migrate.Run(ctx, engine, req)
}
