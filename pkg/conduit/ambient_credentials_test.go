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
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func envWith(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestDefaultConfig_AmbientCredentialsOff anchors the security default.
func TestDefaultConfig_AmbientCredentialsOff(t *testing.T) {
	is := is.New(t)
	cfg := DefaultConfig()
	is.Equal(cfg.Connectors.AmbientCredentials.Enabled, false)
	is.Equal(cfg.Connectors.AmbientCredentials.AllowProxy, false)

	p, warnings, err := cfg.AmbientCredentialsPolicy(envWith(map[string]string{"HTTPS_PROXY": "http://proxy:3128"}))
	is.NoErr(err)
	is.Equal(len(warnings), 0)
	is.Equal(p.AmbientCredentials, false)
	is.Equal(p.CheckResource("anything") != nil, true)
}

func TestAmbientCredentialsPolicy(t *testing.T) {
	type ac struct {
		enabled    bool
		endpoints  []string
		resources  []string
		allowProxy bool
	}
	testCases := []struct {
		name     string
		in       ac
		env      map[string]string
		wantCode conduiterr.Code
		wantPath string
		wantWarn int
		check    func(*is.I, hostpolicy.Policy)
	}{
		{
			name: "enabled with exact resources and endpoints",
			in: ac{
				enabled:   true,
				resources: []string{"acme-ingest-prod", "acme-ingest-staging"},
				endpoints: []string{"https://MinIO.internal:9000", "s3.us-east-1.amazonaws.com"},
			},
			check: func(is *is.I, p hostpolicy.Policy) {
				is.True(p.AmbientCredentials)
				is.Equal(p.AllowResources, []string{"acme-ingest-prod", "acme-ingest-staging"})
				is.Equal(p.AllowEndpoints, []hostpolicy.Endpoint{
					{Host: "minio.internal", Port: "9000"},
					{Host: "s3.us-east-1.amazonaws.com", Port: "443"},
				})
			},
		},
		{
			name: "comma-separated env-style values are split",
			in:   ac{enabled: true, resources: []string{"a,b"}, endpoints: []string{"https://x:1,https://y:2"}},
			check: func(is *is.I, p hostpolicy.Policy) {
				is.Equal(p.AllowResources, []string{"a", "b"})
				is.Equal(len(p.AllowEndpoints), 2)
			},
		},
		{
			name:     "enabled without resources is a startup error",
			in:       ac{enabled: true},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowResourcesKey,
		},
		{
			name:     "wildcard resource",
			in:       ac{enabled: true, resources: []string{"acme-ingest-*"}},
			wantCode: conduiterr.CodeAmbientResourceWildcard,
			wantPath: AmbientCredentialsAllowResourcesKey,
		},
		{
			name:     "wildcard resource fails even when disabled",
			in:       ac{resources: []string{"*"}},
			wantCode: conduiterr.CodeAmbientResourceWildcard,
			wantPath: AmbientCredentialsAllowResourcesKey,
		},
		{
			name:     "ARN resource entry",
			in:       ac{enabled: true, resources: []string{"arn:aws:s3:us-east-1:123456789012:accesspoint/ap"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowResourcesKey,
		},
		{
			name:     "alias resource entry",
			in:       ac{enabled: true, resources: []string{"ap-abc-s3alias"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowResourcesKey,
		},
		{
			name:     "http endpoint",
			in:       ac{enabled: true, resources: []string{"b"}, endpoints: []string{"http://10.0.0.5:9000"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowEndpointsKey,
		},
		{
			name:     "wildcard endpoint",
			in:       ac{enabled: true, resources: []string{"b"}, endpoints: []string{"*.amazonaws.com"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowEndpointsKey,
		},
		{
			name:     "endpoint with path",
			in:       ac{enabled: true, resources: []string{"b"}, endpoints: []string{"https://minio.internal/bucket"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowEndpointsKey,
		},
		{
			name:     "malformed endpoint fails even when disabled",
			in:       ac{endpoints: []string{"ftp://x"}},
			wantCode: conduiterr.CodeInvalidArgument,
			wantPath: AmbientCredentialsAllowEndpointsKey,
		},
		{
			name:     "proxy variable without allow-proxy",
			in:       ac{enabled: true, resources: []string{"b"}},
			env:      map[string]string{"HTTPS_PROXY": "http://user:hunter2@proxy:3128"},
			wantCode: conduiterr.CodeAmbientProxyEnv,
			wantPath: AmbientCredentialsAllowProxyKey,
		},
		{
			name:     "lower-case proxy variable without allow-proxy",
			in:       ac{enabled: true, resources: []string{"b"}},
			env:      map[string]string{"all_proxy": "socks5://proxy:1080"},
			wantCode: conduiterr.CodeAmbientProxyEnv,
			wantPath: AmbientCredentialsAllowProxyKey,
		},
		{
			name:     "proxy variable with allow-proxy warns",
			in:       ac{enabled: true, resources: []string{"b"}, allowProxy: true},
			env:      map[string]string{"HTTPS_PROXY": "http://proxy:3128"},
			wantWarn: 1,
			check:    func(is *is.I, p hostpolicy.Policy) { is.True(p.AllowProxy) },
		},
		{
			name:     "disabled with allowlists is inert and warns",
			in:       ac{resources: []string{"b"}, endpoints: []string{"https://minio.internal:9000"}},
			wantWarn: 1,
			check:    func(is *is.I, p hostpolicy.Policy) { is.Equal(p.AmbientCredentials, false) },
		},
		{
			name:  "disabled ignores proxy variables",
			in:    ac{},
			env:   map[string]string{"HTTP_PROXY": "http://proxy:3128"},
			check: func(is *is.I, p hostpolicy.Policy) { is.Equal(p.AmbientCredentials, false) },
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			cfg := DefaultConfig()
			cfg.Connectors.AmbientCredentials.Enabled = tc.in.enabled
			cfg.Connectors.AmbientCredentials.AllowEndpoints = tc.in.endpoints
			cfg.Connectors.AmbientCredentials.AllowResources = tc.in.resources
			cfg.Connectors.AmbientCredentials.AllowProxy = tc.in.allowProxy

			p, warnings, err := cfg.AmbientCredentialsPolicy(envWith(tc.env))
			if !tc.wantCode.IsZero() {
				ce, ok := conduiterr.Get(err)
				if !ok {
					t.Fatalf("expected ConduitError %s, got %v", tc.wantCode, err)
				}
				is.Equal(ce.Code, tc.wantCode)
				is.Equal(ce.ConfigPath, tc.wantPath)
				is.True(ce.Suggestion != "")
				is.True(!strings.Contains(err.Error(), "hunter2")) // proxy values never leak
				is.Equal(p.AmbientCredentials, false)              // an error never yields an open policy
				return
			}
			is.NoErr(err)
			is.Equal(len(warnings), tc.wantWarn)
			if tc.check != nil {
				tc.check(is, p)
			}
		})
	}
}

// TestValidate_AmbientCredentials checks Config.Validate fails startup on the
// same combinations, and that no setting other than a non-empty resource list
// lets enabled=true start.
func TestValidate_AmbientCredentials(t *testing.T) {
	is := is.New(t)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("all_proxy", "")

	cfg := DefaultConfig()
	is.NoErr(cfg.Validate())

	cfg.Connectors.AmbientCredentials.Enabled = true
	cfg.Connectors.AmbientCredentials.AllowEndpoints = []string{"https://minio.internal:9000"}
	cfg.Connectors.AmbientCredentials.AllowProxy = true
	err := cfg.Validate()
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.ConfigPath, AmbientCredentialsAllowResourcesKey)

	cfg.Connectors.AmbientCredentials.AllowResources = []string{"acme-ingest-prod"}
	is.NoErr(cfg.Validate())

	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	cfg.Connectors.AmbientCredentials.AllowProxy = false
	ce, ok = conduiterr.Get(cfg.Validate())
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeAmbientProxyEnv)
}
