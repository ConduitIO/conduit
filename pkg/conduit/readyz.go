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

package conduit

import (
	"context"
	"net/http"
	"time"

	"github.com/conduitio/conduit/pkg/pipeline"
	json "github.com/goccy/go-json"
)

// readinessStatus is the top-level state reported by /readyz.
const (
	readinessStarting    = "starting"    // engine has not finished starting up
	readinessUnavailable = "unavailable" // engine started but a dependency is down
	readinessReady       = "ready"       // engine can serve
)

// ReadinessResponse is the JSON body returned by /readyz.
type ReadinessResponse struct {
	Status    string              `json:"status"`
	Reason    string              `json:"reason,omitempty"`
	Pipelines *PipelinesReadiness `json:"pipelines,omitempty"`
}

// PipelinesReadiness summarizes pipeline health. Degraded pipelines are reported
// here but do NOT make the engine "not ready" — a degraded pipeline is an
// operational condition, not an inability of the engine to serve requests.
type PipelinesReadiness struct {
	Total    int                 `json:"total"`
	Running  int                 `json:"running"`
	Degraded int                 `json:"degraded"`
	Detail   []PipelineReadiness `json:"degradedPipelines,omitempty"`
}

// PipelineReadiness identifies a degraded pipeline and its recorded error.
type PipelineReadiness struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// readyzHandler serves the /readyz readiness endpoint. It is distinct from the
// liveness/health check: readiness answers "can the engine serve?" and is used by
// orchestrators to gate traffic. It returns 503 only while the engine is still
// starting up or if its state store is unavailable; once the engine can serve it
// returns 200, with any degraded pipelines listed in the body rather than treated
// as not-ready. See docs/design-documents/20260704-phase-1-execution-plan.md.
func (r *Runtime) readyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		resp, code := r.readiness(req.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func (r *Runtime) readiness(ctx context.Context) (ReadinessResponse, int) {
	// The engine has not finished starting up (services initialized, pipelines
	// provisioned, API served) until Ready is closed.
	select {
	case <-r.Ready:
	default:
		return ReadinessResponse{Status: readinessStarting}, http.StatusServiceUnavailable
	}

	// The state store must be reachable for the engine to serve.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.DB.Ping(pingCtx); err != nil {
		return ReadinessResponse{
			Status: readinessUnavailable,
			Reason: "state store unavailable: " + err.Error(),
		}, http.StatusServiceUnavailable
	}

	// Ready. Summarize pipeline health in the body; degraded pipelines do not
	// affect readiness.
	pls := r.pipelineService.List(ctx)
	p := &PipelinesReadiness{Total: len(pls)}
	for _, pl := range pls {
		switch pl.GetStatus() {
		case pipeline.StatusRunning:
			p.Running++
		case pipeline.StatusDegraded:
			p.Degraded++
			p.Detail = append(p.Detail, PipelineReadiness{
				ID:     pl.ID,
				Status: pl.GetStatus().String(),
				Error:  pl.Error,
			})
		case pipeline.StatusSystemStopped, pipeline.StatusUserStopped, pipeline.StatusRecovering:
			// Count toward Total but are neither running nor degraded for the
			// readiness summary.
		}
	}
	return ReadinessResponse{Status: readinessReady, Pipelines: p}, http.StatusOK
}
