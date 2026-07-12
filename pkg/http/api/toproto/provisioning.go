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

package toproto

import (
	"github.com/conduitio/conduit/pkg/provisioning"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

// Diff converts a pkg/provisioning.Diff (the exact result Plan/ApplyPlan/
// ApplyPlanLive return, and the CLI's `deploy`/`apply` render) into its wire
// shape — field for field, no reinterpretation — so the API's PlanPipeline/
// ApplyPipeline responses are identical in substance to the CLI standalone
// path's Diff for the same plan.
func Diff(in provisioning.Diff) *apiv1.Diff {
	changes := make([]*apiv1.Diff_Change, len(in.Changes))
	for i, c := range in.Changes {
		changes[i] = &apiv1.Diff_Change{
			Resource:    string(c.Resource),
			Id:          c.ID,
			Action:      string(c.Action),
			Effect:      string(c.Effect),
			ConfigPaths: c.ConfigPaths,
			Code:        c.Code,
		}
	}
	return &apiv1.Diff{
		PipelineId: in.PipelineID,
		Changes:    changes,
		Hash:       in.Hash,
	}
}
