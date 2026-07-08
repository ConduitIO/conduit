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

	"github.com/conduitio/conduit/pkg/conduit/check"
)

// addrCheck wraps check.AddrBindable to give the result doctor's own stable
// check name (network.grpc / network.http) instead of AddrBindable's
// generic "addr_bindable:<addr>" — the Name --check filters on and the
// human renderer shows.
type addrCheck struct {
	name      string
	addr      string
	configKey string
}

func (c addrCheck) Name() string { return c.name }

func (c addrCheck) Run(ctx context.Context) check.CheckResult {
	result := check.AddrBindable(c.addr, c.configKey).Run(ctx)
	result.Name = c.name
	return result
}

// apiDisabledCheck reports network.grpc/http as a Warn (not skipped
// entirely — every check returns a defined status, per the design doc's
// acceptance criteria) when cfg.API.Enabled is false: there's nothing
// meaningful to probe, since `conduit run` won't bind either address.
type apiDisabledCheck struct {
	name string
}

func (c apiDisabledCheck) Name() string { return c.name }

func (c apiDisabledCheck) Run(context.Context) check.CheckResult {
	return check.CheckResult{
		Status:     check.StatusWarn,
		Category:   check.CategoryNetwork,
		ConfigPath: "api.enabled",
		Message:    fmt.Sprintf("API is disabled (api.enabled=false) — skipping %s", c.name),
	}
}
