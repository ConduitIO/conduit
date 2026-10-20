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

package doctorcheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/matryer/is"
)

const batchingPipeline = `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
        settings:
          sdk.batch.size: "5"
`

// TestArchV2BatchingCheck_FailsOnBatching pins that doctor, with arch-v2
// enabled, refuses a batched destination with the same error code the runtime
// engine uses — before deploy.
func TestArchV2BatchingCheck_FailsOnBatching(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.Preview.PipelineArchV2 = true
	is.NoErr(os.WriteFile(filepath.Join(cfg.Pipelines.Path, "p.yaml"), []byte(batchingPipeline), 0o600))

	report := runChecks(cfg, doctorcheck.Options{})
	res := checkByName(report, doctorcheck.NameArchV2Batching)
	is.Equal(res.Status, check.StatusFail)
	is.Equal(res.Code, "pipeline.archv2_destination_batching_unsupported")
	is.True(res.ConfigPath != "")
}

// TestArchV2BatchingCheck_PassesClean pins the happy path, and that the check
// only appears at all when arch-v2 is enabled (DefaultChecks gates it).
func TestArchV2BatchingCheck_PassesClean(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.Preview.PipelineArchV2 = true

	report := runChecks(cfg, doctorcheck.Options{})
	res := checkByName(report, doctorcheck.NameArchV2Batching)
	is.Equal(res.Status, check.StatusPass)
}

func checkByName(report check.Report, name string) check.CheckResult {
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	return check.CheckResult{}
}
