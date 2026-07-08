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

// Package deploy is the engine behind `conduit pipelines deploy|apply` (see
// docs/design-documents/20260708-cli-pipeline-deploy-apply.md): parsing a
// single pipeline config file, wiring a pkg/provisioning.Service to plan/
// apply against, and rendering the resulting Diff for both --json and human
// output. It is the shared code both cmd/conduit/root/pipelines/deploy.go
// and apply.go are thin cobra shells over, and the seam the future MCP
// `deploy_pipeline` tool (Wave 3) is meant to call 1:1.
package deploy

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"google.golang.org/grpc/codes"
)

// PlanApplier is the interface the `deploy`/`apply` commands depend on —
// *pkg/provisioning.Service satisfies it directly. Declaring it here (rather
// than depending on *provisioning.Service concretely) lets the commands be
// unit-tested against a fake, without a database or plugin runtime.
type PlanApplier interface {
	Plan(ctx context.Context, desired config.Pipeline) (provisioning.Diff, error)
	ApplyPlan(ctx context.Context, desired config.Pipeline, hash string) (provisioning.Diff, error)
}

// CodeMultiPipelineFile is raised when a file resolves to more than one
// pipeline document. Wave 2 (per the design doc's Scope boundary) supports
// exactly one pipeline per file — multi-pipeline-file orchestration is
// deferred alongside `--remote`.
var CodeMultiPipelineFile = conduiterr.Register("provisioning.multi_pipeline_file", codes.InvalidArgument)

// ParseSinglePipeline resolves path (a single .yml/.yaml file — a directory
// is rejected, since deploy/apply operate on exactly one pipeline) and
// returns its one parsed+enriched config.Pipeline.
//
// This is the "parse+validate pre-step" the design doc calls for (§3, AC-12):
// path is first run through the exact same offline parse -> enrich ->
// validate pipeline `conduit pipelines validate` uses (see cmd/conduit/internal/validate),
// so an invalid file is rejected with the same coded findings before any
// Plan/ApplyPlan call — never a partially-planned diff over bad config.
func ParseSinglePipeline(ctx context.Context, path string) (config.Pipeline, error) {
	info, err := os.Stat(path)
	if err != nil {
		ce := conduiterr.Wrap(config.CodeParseError, fmt.Sprintf("could not open %q: %v", path, err), err)
		ce.Suggestion = "check that the file exists and is readable"
		return config.Pipeline{}, ce
	}
	if info.IsDir() {
		ce := conduiterr.New(config.CodeParseError, fmt.Sprintf("%q is a directory: deploy/apply take a single pipeline config file", path))
		ce.Suggestion = "pass the path to one .yml/.yaml file"
		return config.Pipeline{}, ce
	}

	f, err := os.Open(path)
	if err != nil {
		ce := conduiterr.Wrap(config.CodeParseError, fmt.Sprintf("could not open %q: %v", path, err), err)
		ce.Suggestion = "check that the file exists and is readable"
		return config.Pipeline{}, ce
	}
	defer f.Close()

	parser := yaml.NewParser(log.Nop())
	pipelines, err := parser.Parse(ctx, f)
	if err != nil {
		return config.Pipeline{}, err
	}

	switch len(pipelines) {
	case 0:
		ce := conduiterr.New(config.CodeParseError, fmt.Sprintf("%q defines no pipelines", path))
		return config.Pipeline{}, ce
	case 1:
		enriched := config.Enrich(pipelines[0])
		if verr := config.Validate(enriched); verr != nil {
			return config.Pipeline{}, verr
		}
		return enriched, nil
	default:
		ce := conduiterr.New(CodeMultiPipelineFile, fmt.Sprintf(
			"%q defines %d pipelines; deploy/apply support exactly one pipeline per file for now", path, len(pipelines)))
		ce.Suggestion = "split the file into one pipeline per file, or use 'conduit pipelines validate' to check a multi-pipeline file"
		return config.Pipeline{}, ce
	}
}
