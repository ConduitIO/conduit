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

package toproto_test

import (
	"testing"

	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
)

func TestPipelineStoppedReason(t *testing.T) {
	testCases := []struct {
		status pipeline.Status
		want   apiv1.Pipeline_State_StoppedReason
	}{
		{pipeline.StatusUserStopped, apiv1.Pipeline_State_STOPPED_REASON_USER},
		{pipeline.StatusSystemStopped, apiv1.Pipeline_State_STOPPED_REASON_SYSTEM},
		{pipeline.StatusRunning, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
		{pipeline.StatusDegraded, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
		{pipeline.StatusRecovering, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
	}
	for _, tc := range testCases {
		t.Run(tc.status.String(), func(t *testing.T) {
			is := is.New(t)
			is.Equal(toproto.PipelineStoppedReason(tc.status), tc.want)
		})
	}
}

// TestPipeline_StateStoppedReason asserts the full toproto.Pipeline() build wires
// both status and stopped_reason (not just the mapping helper in isolation, so a
// refactor that drops the field from Pipeline() is caught), and that the wire
// Status still collapses both stopped states to STATUS_STOPPED.
func TestPipeline_StateStoppedReason(t *testing.T) {
	testCases := []struct {
		name       string
		status     pipeline.Status
		wantStatus apiv1.Pipeline_Status
		wantReason apiv1.Pipeline_State_StoppedReason
	}{
		{"user-stopped", pipeline.StatusUserStopped, apiv1.Pipeline_STATUS_STOPPED, apiv1.Pipeline_State_STOPPED_REASON_USER},
		{"system-stopped", pipeline.StatusSystemStopped, apiv1.Pipeline_STATUS_STOPPED, apiv1.Pipeline_State_STOPPED_REASON_SYSTEM},
		{"running", pipeline.StatusRunning, apiv1.Pipeline_STATUS_RUNNING, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
		{"degraded", pipeline.StatusDegraded, apiv1.Pipeline_STATUS_DEGRADED, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
		{"recovering", pipeline.StatusRecovering, apiv1.Pipeline_STATUS_RECOVERING, apiv1.Pipeline_State_STOPPED_REASON_UNSPECIFIED},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			pl := &pipeline.Instance{ID: "p1"}
			pl.SetStatus(tc.status)

			got := toproto.Pipeline(pl)
			is.Equal(got.GetState().GetStatus(), tc.wantStatus)
			is.Equal(got.GetState().GetStoppedReason(), tc.wantReason)
		})
	}
}

// TestPipeline_NeverStarted_ReportsUserStopped pins the created-but-never-started
// edge: pipeline.Service.Create initializes an instance to StatusUserStopped, so
// a never-started pipeline reports STATUS_STOPPED / STOPPED_REASON_USER (there is
// no distinct "never started" wire state, and adding one would be a non-additive
// Status enum change out of scope here).
func TestPipeline_NeverStarted_ReportsUserStopped(t *testing.T) {
	is := is.New(t)
	pl := &pipeline.Instance{ID: "p1"}
	pl.SetStatus(pipeline.StatusUserStopped) // as pipeline.Service.Create sets it

	got := toproto.Pipeline(pl)
	is.Equal(got.GetState().GetStatus(), apiv1.Pipeline_STATUS_STOPPED)
	is.Equal(got.GetState().GetStoppedReason(), apiv1.Pipeline_State_STOPPED_REASON_USER)
}
