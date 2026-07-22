// Copyright © 2022 Meroxa, Inc.
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

package api

import (
	"context"
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/http/api/mock"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPipelineAPIv1_CreatePipeline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	config := pipeline.Config{
		Name:        "test-pipeline",
		Description: "description of my test pipeline",
	}
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: config,
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	psMock.EXPECT().Create(ctx, config).Return(pl, nil).Times(1)

	want := &apiv1.CreatePipelineResponse{
		Pipeline: &apiv1.Pipeline{
			Id: pl.ID,
			State: &apiv1.Pipeline_State{
				Status:        apiv1.Pipeline_STATUS_STOPPED,
				StoppedReason: apiv1.Pipeline_State_STOPPED_REASON_SYSTEM,
			},
			Config: &apiv1.Pipeline_Config{
				Name:        config.Name,
				Description: config.Description,
			},
			CreatedAt: timestamppb.New(pl.CreatedAt),
			UpdatedAt: timestamppb.New(pl.UpdatedAt),
		},
	}
	got, err := api.CreatePipeline(
		ctx,
		&apiv1.CreatePipelineRequest{
			Config: want.Pipeline.Config,
		},
	)

	is.NoErr(err)
	is.Equal(got, want)
}

func TestPipelineAPIv1_StopPipeline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	id := uuid.NewString()
	force := true
	psMock.EXPECT().Stop(ctx, id, force).Return(nil).Times(1)

	got, err := api.StopPipeline(ctx, &apiv1.StopPipelineRequest{Id: id, Force: force})

	is.NoErr(err)
	is.Equal(got, &apiv1.StopPipelineResponse{})
}

func TestPipelineAPIv1_ListPipelinesByName(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	names := []string{"do-not-want-this-pipeline", "want-p1", "want-p2", "skip", "another-skipped"}
	pls := make([]*pipeline.Instance, 0)
	plsMap := make(map[string]*pipeline.Instance)

	for _, name := range names {
		p := &pipeline.Instance{
			ID:     uuid.NewString(),
			Config: pipeline.Config{Name: name},
		}
		pls = append(pls, p)
		plsMap[p.ID] = p
	}

	psMock.EXPECT().
		List(ctx).
		Return(plsMap).
		Times(1)

	want := &apiv1.ListPipelinesResponse{
		Pipelines: []*apiv1.Pipeline{{
			Id:    pls[1].ID,
			State: &apiv1.Pipeline_State{},
			Config: &apiv1.Pipeline_Config{
				Name: pls[1].Config.Name,
			},
			CreatedAt: timestamppb.New(pls[1].CreatedAt),
			UpdatedAt: timestamppb.New(pls[1].UpdatedAt),
		}, {
			Id:    pls[2].ID,
			State: &apiv1.Pipeline_State{},
			Config: &apiv1.Pipeline_Config{
				Name: pls[2].Config.Name,
			},
			CreatedAt: timestamppb.New(pls[2].CreatedAt),
			UpdatedAt: timestamppb.New(pls[2].UpdatedAt),
		}},
	}

	got, err := api.ListPipelines(
		ctx,
		&apiv1.ListPipelinesRequest{Name: "want-.*"},
	)
	is.NoErr(err)

	sortPipelines(want.Pipelines)
	sortPipelines(got.Pipelines)
	is.Equal(got, want)
}

func sortPipelines(p []*apiv1.Pipeline) {
	sort.Slice(p, func(i, j int) bool {
		return p[i].Id < p[j].Id
	})
}

// TestPipelineAPIv1_PlanPipeline_ReadOnly is the regression test for AC-1:
// PlanPipeline calls Provisioner.Plan (never ApplyPlanLive — no expectation
// is set on it, so gomock fails the test if PlanPipeline attempts to mutate
// anything) and returns its Diff translated field-for-field via toproto.Diff.
func TestPipelineAPIv1_PlanPipeline_ReadOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	diff := provisioning.Diff{
		PipelineID: "p1",
		Hash:       "abc123",
		Changes: []provisioning.Change{
			{Resource: provisioning.ResourceConnector, ID: "p1:conn:src", Action: provisioning.ChangeActionUpdate, Effect: provisioning.EffectInPlace, ConfigPaths: []string{"settings.table"}, Code: "provisioning.connector.update"},
		},
	}
	provMock.EXPECT().Plan(ctx, gomock.Any()).Return(diff, nil)

	got, err := api.PlanPipeline(ctx, &apiv1.PlanPipelineRequest{
		Config: &apiv1.PipelineDocument{Id: "p1"},
	})
	is.NoErr(err)
	is.Equal(got.Diff.PipelineId, "p1")
	is.Equal(got.Diff.Hash, "abc123")
	is.Equal(len(got.Diff.Changes), 1)
	is.Equal(got.Diff.Changes[0].Resource, "connector")
	is.Equal(got.Diff.Changes[0].Effect, "in_place")
	is.Equal(got.Diff.Changes[0].ConfigPaths, []string{"settings.table"})
}

// TestPipelineAPIv1_ApplyPipeline_PassesServerSideAuthorizationFlag is the
// regression test proving the operator-authorization gate is wired from
// server construction, not the request: ApplyPipelineRequest has no field
// for it (see api.proto), so the ONLY way ApplyPipeline can pass true to
// Provisioner.ApplyPlanLive's allowRestartOnRunning parameter is the value
// NewPipelineAPIv1 was constructed with. This test constructs two API
// instances from the identical request and asserts each calls
// ApplyPlanLive with the flag it was constructed with — proving an RPC
// caller cannot influence it.
func TestPipelineAPIv1_ApplyPipeline_PassesServerSideAuthorizationFlag(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "flag_off", true: "flag_on"}[allow], func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			psMock := mock.NewPipelineOrchestrator(ctrl)
			provMock := mock.NewProvisioner(ctrl)
			api := NewPipelineAPIv1(psMock, provMock, allow)

			var gotAllow bool
			provMock.EXPECT().ApplyPlanLive(ctx, gomock.Any(), "abc123", gomock.Any()).
				DoAndReturn(func(_ context.Context, _ config.Pipeline, _ string, allowRestartOnRunning bool) (provisioning.Diff, error) {
					gotAllow = allowRestartOnRunning
					return provisioning.Diff{PipelineID: "p1", Hash: "abc123"}, nil
				})

			_, err := api.ApplyPipeline(ctx, &apiv1.ApplyPipelineRequest{
				Config: &apiv1.PipelineDocument{Id: "p1"},
				Hash:   "abc123",
				// Note: no field on ApplyPipelineRequest can set the
				// authorization flag — this is the point of the test.
			})
			is.NoErr(err)
			is.Equal(gotAllow, allow)
		})
	}
}

// TestPipelineAPIv1_ApplyPipeline_GateDenied_SurfacesCodedError proves a
// provisioning.CodeLiveApplyUnauthorized error from ApplyPlanLive (the
// enforced data-path gate refusing a restart-class apply against a running
// pipeline) surfaces through the RPC as a FailedPrecondition status, not a
// generic/unknown error — an agent driving the API can tell "refused by
// policy" apart from "server error".
func TestPipelineAPIv1_ApplyPipeline_GateDenied_SurfacesCodedError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	wantErr := conduiterr.New(provisioning.CodeLiveApplyUnauthorized, "pipeline \"p1\" is running and this plan includes a restart-class change")
	provMock.EXPECT().ApplyPlanLive(ctx, gomock.Any(), "abc123", false).Return(provisioning.Diff{}, wantErr)

	_, err := api.ApplyPipeline(ctx, &apiv1.ApplyPipelineRequest{
		Config: &apiv1.PipelineDocument{Id: "p1"},
		Hash:   "abc123",
	})
	is.True(err != nil)

	st, ok := grpcstatus.FromError(err)
	is.True(ok)
	is.Equal(st.Code(), codes.FailedPrecondition)
}

// dlqSecretSentinel is planted in a DLQ's Settings across the tests below
// (#2640, the exact leak site this issue was filed against — PipelineDLQ
// returned pl.Settings unredacted). If it ever shows up in a GetDLQ/UpdateDLQ
// response, the redaction at toproto.PipelineDLQ regressed.
const dlqSecretSentinel = "SENTINEL_dlq_aws_secret_9f3a"

// TestPipelineAPIv1_GetDLQ_RedactsSettings is the regression test for #2640:
// GetDLQ must never echo back a DLQ Settings value, only "***", while Plugin
// and the window fields stay untouched.
func TestPipelineAPIv1_GetDLQ_RedactsSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	pl := &pipeline.Instance{
		ID: "p1",
		DLQ: pipeline.DLQ{
			Plugin:              "builtin:s3",
			Settings:            map[string]string{"aws.secretAccessKey": dlqSecretSentinel},
			WindowSize:          10,
			WindowNackThreshold: 5,
		},
	}
	psMock.EXPECT().Get(ctx, "p1").Return(pl, nil).Times(1)

	got, err := api.GetDLQ(ctx, &apiv1.GetDLQRequest{Id: "p1"})
	is.NoErr(err)

	is.Equal(got.Dlq.Plugin, "builtin:s3") // structural field must be untouched
	is.Equal(got.Dlq.WindowSize, uint64(10))
	is.Equal(got.Dlq.WindowNackThreshold, uint64(5))
	is.Equal(len(got.Dlq.Settings), 1)
	is.True(got.Dlq.Settings["aws.secretAccessKey"] != dlqSecretSentinel) // secret leaked
	is.Equal(got.Dlq.Settings["aws.secretAccessKey"], "***")
}

// TestPipelineAPIv1_UpdateDLQ_RedactsSettings is the same regression as
// GetDLQ's, but for the UpdateDLQ response: the updated DLQ's Settings must
// come back redacted even though the *request* carried the real value (the
// API redacts what it hands back, never what a config-as-code/API client
// sends it).
func TestPipelineAPIv1_UpdateDLQ_RedactsSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	provMock := mock.NewProvisioner(ctrl)
	api := NewPipelineAPIv1(psMock, provMock, false)

	reqDLQ := pipeline.DLQ{
		Plugin:   "builtin:s3",
		Settings: map[string]string{"aws.secretAccessKey": dlqSecretSentinel},
	}
	updated := &pipeline.Instance{ID: "p1", DLQ: reqDLQ}
	psMock.EXPECT().UpdateDLQ(ctx, "p1", reqDLQ).Return(updated, nil).Times(1)

	got, err := api.UpdateDLQ(ctx, &apiv1.UpdateDLQRequest{
		Id: "p1",
		Dlq: &apiv1.Pipeline_DLQ{
			Plugin:   reqDLQ.Plugin,
			Settings: reqDLQ.Settings,
		},
	})
	is.NoErr(err)

	is.Equal(got.Dlq.Plugin, "builtin:s3")
	is.Equal(len(got.Dlq.Settings), 1)
	is.True(got.Dlq.Settings["aws.secretAccessKey"] != dlqSecretSentinel) // secret leaked
	is.Equal(got.Dlq.Settings["aws.secretAccessKey"], "***")
}
