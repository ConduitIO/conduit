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

package doctorcheck

import (
	"context"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
)

// archV2BatchingCheck reuses the offline validate engine (the same one
// `conduit pipelines validate` shells over) to refuse a destination or
// dead-letter-queue with sdk.batch.size above 1 when arch-v2 is enabled. It
// shares config.SDKBatchSizeAboveOne with the runtime engine guard, so doctor
// never drifts from what `conduit run` refuses at start (#2979, #2985).
type archV2BatchingCheck struct {
	cfg conduit.Config
}

func (c archV2BatchingCheck) Name() string { return NameArchV2Batching }

func (c archV2BatchingCheck) Run(ctx context.Context) check.CheckResult {
	report, err := validate.RunWithOptions(ctx, c.cfg.Pipelines.Path, validate.Options{ArchV2: true})
	if err != nil {
		return check.CheckResult{
			Status:   check.StatusWarn,
			Category: CategoryConfig,
			Message:  fmt.Sprintf("could not check pipelines at %s: %v", c.cfg.Pipelines.Path, err),
		}
	}

	// Collect the arch-v2 findings only; parse/validate errors are
	// config.validate's job, not this check's.
	var msgs []string
	var firstPath string
	for _, f := range report.Files {
		for _, finding := range f.Findings {
			if finding.Code != "pipeline.archv2_destination_batching_unsupported" {
				continue
			}
			msgs = append(msgs, finding.Message)
			if firstPath == "" {
				firstPath = finding.ConfigPath
			}
		}
	}

	if len(msgs) == 0 {
		return check.CheckResult{
			Status:   check.StatusPass,
			Category: CategoryConfig,
			Message:  "no destination or dead-letter-queue sets sdk.batch.size above 1",
		}
	}

	return check.CheckResult{
		Status:     check.StatusFail,
		Category:   CategoryConfig,
		Code:       "pipeline.archv2_destination_batching_unsupported",
		ConfigPath: firstPath,
		Message:    strings.Join(msgs, "; "),
		Suggestion: "set sdk.batch.size to 1 or remove it, or disable preview.pipeline-arch-v2",
	}
}
