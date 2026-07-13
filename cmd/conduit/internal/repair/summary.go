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

package repair

// PlanSummary is the --json/MCP envelope's `summary` payload for a read
// -mode (Collect/CollectContent) repair result — a rollup of Plan.Fixes by
// FixClass. Lives here (not in the CLI or MCP shell) so both call the exact
// same rollup, mirroring cmd/conduit/internal/deploy.Summary/Summarize.
type PlanSummary struct {
	Safe     int `json:"safe"`
	Restart  int `json:"restart"`
	DataPath int `json:"dataPath"`
}

// SummarizePlan rolls plan.Fixes up by FixClass.
func SummarizePlan(plan Plan) PlanSummary {
	var s PlanSummary
	for _, f := range plan.Fixes {
		switch f.Class {
		case FixClassSafe:
			s.Safe++
		case FixClassRestart:
			s.Restart++
		case FixClassDataPath:
			s.DataPath++
		}
	}
	return s
}

// ApplySummary is the --json/MCP envelope's `summary` payload for an
// Apply result — a rollup of Result.Fixes by outcome.
type ApplySummary struct {
	Applied int `json:"applied"`
	Skipped int `json:"skipped"`
}

// SummarizeResult rolls res.Fixes up by FixOutcome.
func SummarizeResult(res Result) ApplySummary {
	var s ApplySummary
	for _, f := range res.Fixes {
		switch f.Outcome {
		case FixOutcomeApplied:
			s.Applied++
		case FixOutcomeSkipped:
			s.Skipped++
		}
	}
	return s
}
