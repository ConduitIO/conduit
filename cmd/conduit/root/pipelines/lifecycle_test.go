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

package pipelines

import (
	"testing"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// TestPipelineStatusLabel pins the 5-value status vocabulary mapping,
// including the collapsed-STOPPED disambiguation described on
// pipelineStatusLabel's doc: a "stop" action's STATUS_STOPPED read-back is
// always "UserStopped", never "SystemStopped" (which this RPC path can never
// produce).
func TestPipelineStatusLabel(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name   string
		action string
		status apiv1.Pipeline_Status
		want   string
	}{
		{"start running", "start", apiv1.Pipeline_STATUS_RUNNING, "Running"},
		{"stop stopped", "stop", apiv1.Pipeline_STATUS_STOPPED, "UserStopped"},
		{"start unexpectedly stopped", "start", apiv1.Pipeline_STATUS_STOPPED, "Stopped"},
		{"degraded", "start", apiv1.Pipeline_STATUS_DEGRADED, "Degraded"},
		{"recovering", "stop", apiv1.Pipeline_STATUS_RECOVERING, "Recovering"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is.Equal(pipelineStatusLabel(tt.action, tt.status), tt.want)
		})
	}
}

// TestLifecycleResult_JSON pins AC-2/AC-4's literal --json shapes: no stray
// "force" key on a start result, and force:true present on a forced stop.
func TestLifecycleResult_JSON(t *testing.T) {
	is := is.New(t)

	start := &LifecycleResult{PipelineID: "orders", Action: "start", Status: "Running"}
	b, err := json.Marshal(start)
	is.NoErr(err)
	is.Equal(string(b), `{"pipeline_id":"orders","action":"start","status":"Running"}`)

	stop := &LifecycleResult{PipelineID: "orders", Action: "stop", Status: "UserStopped"}
	b, err = json.Marshal(stop)
	is.NoErr(err)
	is.Equal(string(b), `{"pipeline_id":"orders","action":"stop","status":"UserStopped"}`)

	forced := &LifecycleResult{PipelineID: "orders", Action: "stop", Force: true, Status: "UserStopped"}
	b, err = json.Marshal(forced)
	is.NoErr(err)
	is.Equal(string(b), `{"pipeline_id":"orders","action":"stop","force":true,"status":"UserStopped"}`)
}

// TestRenderLifecycleResult_WrongType asserts Render never panics on an
// unexpected result type (defensive: the decorator always passes back
// whatever ExecuteWithClientResult returned, but Render's signature is `any`).
func TestRenderLifecycleResult_WrongType(t *testing.T) {
	is := is.New(t)
	is.Equal(renderLifecycleResult("not a LifecycleResult"), "")
}
