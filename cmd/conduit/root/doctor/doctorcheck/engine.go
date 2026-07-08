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
	"time"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// engineTimeout bounds how long engineReachableCheck waits for a health
// response — short, since a healthy local server answers almost instantly
// and the common case (nothing running yet) should not make `doctor` feel
// slow.
const engineTimeout = 5 * time.Second

// engineReachableCheck asks whether a running Conduit server answers a
// health check at cfg.API.GRPC.Address, via the same api.Client.CheckHealth
// call the rest of the CLI uses to talk to a running instance.
//
// CheckHealth (see cmd/conduit/api/client.go) cannot distinguish "no server
// is listening" from "a server is listening but reports unhealthy" — both
// surface as the same conduiterr.CodeUnavailable error. This check does not
// pretend otherwise: it reports a binary pass/fail (well, pass/warn-or-fail)
// on reachability, not a three-way "down vs unhealthy vs fine" split.
type engineReachableCheck struct {
	cfg           conduit.Config
	requireServer bool
}

func (c engineReachableCheck) Name() string { return NameEngineReachable }

func (c engineReachableCheck) Timeout() time.Duration { return engineTimeout }

func (c engineReachableCheck) Run(ctx context.Context) check.CheckResult {
	if !c.cfg.API.Enabled {
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryEngine,
			ConfigPath: "api.enabled",
			Message:    "API is disabled (api.enabled=false) — nothing to check",
		}
	}

	addr := c.cfg.API.GRPC.Address

	client, err := api.NewClient(ctx, addr)
	if err != nil {
		status := check.StatusWarn
		if c.requireServer {
			status = check.StatusFail
		}
		return check.CheckResult{
			Status:     status,
			Category:   CategoryEngine,
			Code:       conduiterr.CodeUnavailable.Reason(),
			ConfigPath: configKeyAPIGRPCAddress,
			Message:    fmt.Sprintf("no Conduit server answering at %s (this is expected if you haven't run `conduit run` yet)", addr),
			Suggestion: "start Conduit with `conduit run`, or check api.grpc.address if you expected one to already be running",
		}
	}
	defer client.Close()

	return check.CheckResult{
		Status:     check.StatusPass,
		Category:   CategoryEngine,
		ConfigPath: configKeyAPIGRPCAddress,
		Message:    fmt.Sprintf("Conduit server is serving at %s", addr),
	}
}
