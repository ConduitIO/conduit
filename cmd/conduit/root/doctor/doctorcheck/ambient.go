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
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// ambientCredentialsCheck shows the effective connectors.ambient-credentials.*
// policy and fails on the combinations `conduit run` refuses at startup, so a
// bad config is found before the engine starts. It uses the same translation
// the engine uses (conduit.Config.AmbientCredentialsPolicy). Allowlisted
// endpoints and resources are shown; they are operator config, not secrets.
// Proxy variables are named, never their values.
type ambientCredentialsCheck struct {
	cfg conduit.Config
	// environ returns the environment to read proxy variables from; nil means
	// os.Environ.
	environ func() []string
}

func (c ambientCredentialsCheck) Name() string { return NameConnectorsAmbientCredentials }

func (c ambientCredentialsCheck) getenv() func(string) string {
	if c.environ == nil {
		return os.Getenv
	}
	env := map[string]string{}
	for _, kv := range c.environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return func(k string) string { return env[k] }
}

func (c ambientCredentialsCheck) Run(context.Context) check.CheckResult {
	p, warnings, err := c.cfg.AmbientCredentialsPolicy(c.getenv())
	if err != nil {
		res := check.CheckResult{
			Status:     check.StatusFail,
			Category:   CategoryConfig,
			Code:       conduiterr.CodeInvalidArgument.Reason(),
			ConfigPath: conduit.AmbientCredentialsEnabledKey,
			Message:    err.Error(),
		}
		if ce, ok := conduiterr.Get(err); ok {
			res.Code = ce.Code.Reason()
			res.ConfigPath = ce.ConfigPath
			res.Suggestion = ce.Suggestion
		}
		return res
	}

	if !p.AmbientCredentials {
		res := check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryConfig,
			ConfigPath: conduit.AmbientCredentialsEnabledKey,
			Message:    "built-in connectors may not use this host's ambient cloud credentials (default)",
		}
		if len(warnings) > 0 {
			res.Status = check.StatusWarn
			res.Code = conduiterr.CodeInvalidArgument.Reason()
			res.Message = strings.Join(warnings, "; ")
			res.Suggestion = "remove the inert keys, or set " + conduit.AmbientCredentialsEnabledKey + "=true"
		}
		return res
	}

	endpoints := make([]string, 0, len(p.AllowEndpoints))
	for _, e := range p.AllowEndpoints {
		endpoints = append(endpoints, e.String())
	}
	endpointText := "none (only the cloud SDK's own endpoints)"
	if len(endpoints) > 0 {
		endpointText = strings.Join(endpoints, ", ")
	}
	msg := fmt.Sprintf("built-in connectors may use this host's ambient cloud credentials; "+
		"allow-resources: %s; allow-endpoints: %s; allow-proxy: %t; applies only to builtin: connectors",
		strings.Join(p.AllowResources, ", "), endpointText, p.AllowProxy)

	res := check.CheckResult{
		Status:     check.StatusPass,
		Category:   CategoryConfig,
		ConfigPath: conduit.AmbientCredentialsEnabledKey,
		Message:    msg,
	}
	if len(warnings) > 0 {
		res.Status = check.StatusWarn
		res.Code = conduiterr.CodeAmbientProxyEnv.Reason()
		res.ConfigPath = conduit.AmbientCredentialsAllowProxyKey
		res.Message = msg + "; " + strings.Join(warnings, "; ")
		res.Suggestion = "keep allow-proxy only if the proxy is trusted to reach intended destinations (see " +
			conduit.AmbientCredentialsDocsPath + ")"
	}
	return res
}
