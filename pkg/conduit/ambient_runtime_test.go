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
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// policyProbe records the policy a built-in connector sees at Configure.
type policyProbe struct {
	mu   sync.Mutex
	seen *hostpolicy.Policy
}

type probeSourceConfig struct {
	sdk.UnimplementedSourceConfig
	probe *policyProbe
}

func (c *probeSourceConfig) Validate(ctx context.Context) error {
	p := hostpolicy.FromContext(ctx)
	c.probe.mu.Lock()
	defer c.probe.mu.Unlock()
	c.probe.seen = &p
	return nil
}

type probeSource struct {
	sdk.UnimplementedSource
	cfg *probeSourceConfig
}

func (s *probeSource) Config() sdk.SourceConfig { return s.cfg }

func probeConnector(p *policyProbe) sdk.Connector {
	return sdk.Connector{
		NewSpecification: func() pconnector.Specification {
			return pconnector.Specification{Name: "ambientprobe", Version: "v0.1.0"}
		},
		NewSource: func() sdk.Source { return &probeSource{cfg: &probeSourceConfig{probe: p}} },
	}
}

// TestRuntime_AmbientPolicyReachesBuiltinConnectors is the end of the chain
// the adapter tests start at: conduit.Config -> NewRuntime/createServices ->
// builtin registry -> the call context of a built-in connector.
func TestRuntime_AmbientPolicyReachesBuiltinConnectors(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "")
	}
	testCases := []struct {
		name    string
		enable  bool
		wantOn  bool
		wantRes []string
	}{
		{name: "operator enabled", enable: true, wantOn: true, wantRes: []string{"acme-ingest-prod"}},
		{name: "default config", enable: false, wantOn: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			probe := &policyProbe{}

			cfg := DefaultConfigWithBasePath(t.TempDir())
			cfg.DB.Type = DBTypeInMemory
			cfg.Pipelines.Path = t.TempDir()
			cfg.Log.NewLogger = func(string, string) log.CtxLogger { return log.Nop() }
			cfg.ConnectorPlugins = map[string]sdk.Connector{"example.com/ambientprobe": probeConnector(probe)}
			if tc.enable {
				cfg.Connectors.AmbientCredentials.Enabled = true
				cfg.Connectors.AmbientCredentials.AllowResources = []string{"acme-ingest-prod"}
				cfg.Connectors.AmbientCredentials.AllowEndpoints = []string{"https://minio.internal:9000"}
			}

			r, err := NewRuntime(cfg)
			is.NoErr(err)
			t.Cleanup(func() { _ = r.DB.Close() })
			r.connectorPluginService.Init(ctx, "", cfg.Connectors.MaxReceiveRecordSize)

			d, err := r.connectorPluginService.NewDispenser(log.Nop(), "builtin:ambientprobe", "conn-1")
			is.NoErr(err)
			src, err := d.DispenseSource()
			is.NoErr(err)
			_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{})
			is.NoErr(err)

			probe.mu.Lock()
			defer probe.mu.Unlock()
			is.True(probe.seen != nil)
			is.Equal(probe.seen.AmbientCredentials, tc.wantOn)
			if tc.wantOn {
				is.Equal(probe.seen.AllowResources, tc.wantRes)
				is.Equal(probe.seen.AllowEndpoints, []hostpolicy.Endpoint{{Host: "minio.internal", Port: "9000"}})
			}
		})
	}
}
