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

package deploy

import (
	"context"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

// probeTimeout bounds how long NewService waits to find out whether a
// Conduit server is reachable at cfg.API.GRPC.Address before falling back to
// NewLocalService. deploy/apply are one-shot CLI commands; a hung probe
// against a misconfigured/firewalled address (rather than a fast "connection
// refused") must not make them hang indefinitely.
const probeTimeout = 3 * time.Second

// RemoteService is a PlanApplier backed by a running Conduit server's
// PlanPipeline/ApplyPipeline RPCs (see
// docs/design-documents/20260708-live-server-deploy-apply.md) instead of a
// directly-opened local store (NewLocalService). Unlike the standalone path
// — which refuses outright to touch a pipeline it believes is running, see
// NewLocalService's doc on why that belief can't always be trusted anyway —
// RemoteService's ApplyPlan can apply to a genuinely RUNNING pipeline: the
// server (which owns the pipeline's actual lifecycle) drives a graceful
// stop-drain-restart via provisioning.Service.ApplyPlanLive. Whether a
// restart-class change is *authorized* against a running pipeline is a
// server-side operator decision (conduit.Config.API.AllowLiveRestartApply);
// this client has no say in it and gets back a coded
// provisioning.live_apply_unauthorized error if it isn't authorized.
type RemoteService struct {
	client *api.Client
}

// NewRemoteService wraps an already-connected, already-health-checked client
// as a PlanApplier. The caller owns the client's lifecycle (Close).
func NewRemoteService(client *api.Client) *RemoteService {
	return &RemoteService{client: client}
}

// Plan implements PlanApplier by calling the PlanPipeline RPC — read-only,
// identical semantics to provisioning.Service.Plan (see PipelineAPIv1.PlanPipeline
// server-side), just reached over the wire instead of a local store.
func (s *RemoteService) Plan(ctx context.Context, desired config.Pipeline) (provisioning.Diff, error) {
	resp, err := s.client.PipelineServiceClient.PlanPipeline(ctx, &apiv1.PlanPipelineRequest{
		Config: pipelineDocument(desired),
	})
	if err != nil {
		return provisioning.Diff{}, cerrors.Errorf("failed to plan pipeline against the live server: %w", err)
	}
	return diffFromProto(resp.GetDiff()), nil
}

// ApplyPlan implements PlanApplier by calling the ApplyPipeline RPC. Against
// a running pipeline with a restart-class change, this can succeed where the
// standalone ApplyPlan always refuses — see RemoteService's doc — or fail
// with provisioning.live_apply_unauthorized if the server wasn't started
// with the operator authorization flag; either way the coded error surfaces
// as-is (wrapped, not swallowed) for cecdysis's exit-code mapping.
func (s *RemoteService) ApplyPlan(ctx context.Context, desired config.Pipeline, hash string) (provisioning.Diff, error) {
	resp, err := s.client.PipelineServiceClient.ApplyPipeline(ctx, &apiv1.ApplyPipelineRequest{
		Config: pipelineDocument(desired),
		Hash:   hash,
	})
	if err != nil {
		return provisioning.Diff{}, cerrors.Errorf("failed to apply pipeline against the live server: %w", err)
	}
	return diffFromProto(resp.GetDiff()), nil
}

// pipelineDocument converts a config.Pipeline (the parsed/enriched/validated
// desired state deploy.ParseSinglePipeline produces) to its wire shape —
// the exact inverse of pkg/http/api/fromproto.PipelineDocument, which the
// server-side handler applies to get back the same config.Pipeline this
// function started from.
func pipelineDocument(in config.Pipeline) *apiv1.PipelineDocument {
	connectors := make([]*apiv1.PipelineDocument_Connector, len(in.Connectors))
	for i, c := range in.Connectors {
		processors := make([]*apiv1.PipelineDocument_Processor, len(c.Processors))
		for j, p := range c.Processors {
			processors[j] = pipelineDocumentProcessor(p)
		}
		connectors[i] = &apiv1.PipelineDocument_Connector{
			Id:         c.ID,
			Type:       c.Type,
			Plugin:     c.Plugin,
			Name:       c.Name,
			Settings:   c.Settings,
			Processors: processors,
		}
	}

	processors := make([]*apiv1.PipelineDocument_Processor, len(in.Processors))
	for i, p := range in.Processors {
		processors[i] = pipelineDocumentProcessor(p)
	}

	var dlq *apiv1.PipelineDocument_DLQ
	if in.DLQ.WindowSize != nil || in.DLQ.WindowNackThreshold != nil || in.DLQ.Plugin != "" || len(in.DLQ.Settings) > 0 {
		dlq = &apiv1.PipelineDocument_DLQ{
			Plugin:   in.DLQ.Plugin,
			Settings: in.DLQ.Settings,
		}
		if in.DLQ.WindowSize != nil {
			dlq.WindowSize = uint64(*in.DLQ.WindowSize) //nolint:gosec // no risk of overflow, already-validated config
		}
		if in.DLQ.WindowNackThreshold != nil {
			dlq.WindowNackThreshold = uint64(*in.DLQ.WindowNackThreshold) //nolint:gosec // no risk of overflow, already-validated config
		}
	}

	return &apiv1.PipelineDocument{
		Id:          in.ID,
		Status:      in.Status,
		Name:        in.Name,
		Description: in.Description,
		Connectors:  connectors,
		Processors:  processors,
		Dlq:         dlq,
	}
}

func pipelineDocumentProcessor(in config.Processor) *apiv1.PipelineDocument_Processor {
	return &apiv1.PipelineDocument_Processor{
		Id:        in.ID,
		Plugin:    in.Plugin,
		Settings:  in.Settings,
		Workers:   int32(in.Workers), //nolint:gosec // no risk of overflow, already-validated config
		Condition: in.Condition,
	}
}

// diffFromProto converts an *apiv1.Diff back to provisioning.Diff — the
// exact inverse of pkg/http/api/toproto.Diff — so RemoteService returns the
// same type deploy/apply's rendering (deploy.Render/Summarize) already knows
// how to display, regardless of which PlanApplier produced it.
func diffFromProto(in *apiv1.Diff) provisioning.Diff {
	if in == nil {
		return provisioning.Diff{}
	}
	changes := make([]provisioning.Change, len(in.Changes))
	for i, c := range in.Changes {
		changes[i] = provisioning.Change{
			Resource:    provisioning.Resource(c.Resource),
			ID:          c.Id,
			Action:      provisioning.ChangeAction(c.Action),
			Effect:      provisioning.Effect(c.Effect),
			ConfigPaths: c.ConfigPaths,
			Code:        c.Code,
		}
	}
	return provisioning.Diff{
		PipelineID: in.PipelineId,
		Changes:    changes,
		Hash:       in.Hash,
	}
}

// NewService is deploy/apply's PlanApplier factory: it prefers a live
// Conduit server (reachable at cfg.API.GRPC.Address) so apply can reach a
// running pipeline via ApplyPlanLive, and falls back to NewLocalService
// (Badger, stopped-only) when no server answers — see
// docs/design-documents/20260708-live-server-deploy-apply.md §3. Both
// branches satisfy the same PlanApplier interface with the same Diff/error
// shape, so the caller (DeployCommand/ApplyCommand) doesn't need to know
// which transport served the request; only the underlying behavior differs
// (RemoteService can apply to a running pipeline, NewLocalService never can).
//
// The probe is a bounded-timeout api.NewClient call (which itself performs a
// health check) rather than a bare TCP dial: a process listening on the port
// but not actually serving the Conduit gRPC API (or serving an incompatible
// version) must fall back to standalone, not hand back a client that then
// fails confusingly on the first real RPC.
func NewService(ctx context.Context, cfg conduit.Config) (PlanApplier, func() error, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	client, err := api.NewClient(probeCtx, cfg.API.GRPC.Address)
	cancel()
	if err == nil {
		return NewRemoteService(client), client.Close, nil
	}

	// No live server reachable at cfg.API.GRPC.Address (connection refused,
	// timed out, or answered but unhealthy) — fall back to the standalone
	// path exactly as before this function existed.
	return NewLocalService(ctx, cfg)
}
