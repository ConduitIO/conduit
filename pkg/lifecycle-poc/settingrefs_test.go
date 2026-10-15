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

package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
)

// TestStart_SettingRefs_RefusedOnArchV2 checks the v0.20.2 downgrade guard on
// the arch-v2 lifecycle: a record written by v0.21 or later (SettingRefs) is
// refused with the coded error, the pipeline is Degraded, and no plugin
// dispenser is requested.
func TestStart_SettingRefs_RefusedOnArchV2(t *testing.T) {
	cases := map[string]func(pl *pipeline.Instance, c *connector.Instance, p *processor.Instance){
		"connector": func(_ *pipeline.Instance, c *connector.Instance, _ *processor.Instance) {
			c.Config.SettingRefs = map[string]string{"k": "${V}"}
		},
		"processor": func(_ *pipeline.Instance, _ *connector.Instance, p *processor.Instance) {
			p.Config.SettingRefs = map[string]string{"k": "${V}"}
		},
		"dlq": func(pl *pipeline.Instance, _ *connector.Instance, _ *processor.Instance) {
			pl.DLQ.SettingRefs = map[string]string{"k": "${V}"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ps := pipeline.NewService(log.Nop(), &inmemory.DB{})
			pl, err := ps.Create(ctx, "p1", pipeline.Config{Name: "my-pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)
			c := &connector.Instance{ID: "c1", Type: connector.TypeSource}
			p := &processor.Instance{ID: "proc1"}
			pl.ConnectorIDs = []string{c.ID}
			pl.ProcessorIDs = []string{p.ID}
			mutate(pl, c, p)

			plugins := &countingPlugins{}
			ls := NewService(log.Nop(), testErrRecoveryCfg(), testConnectorService{c.ID: c}, testProcessorService{p.ID: p}, plugins, ps, true)

			err = ls.Start(ctx, pl.ID)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, pipeline.CodeSettingsReferencesUnsupported)
			is.True(strings.Contains(ce.Message, "my-pipeline"))
			is.Equal(pl.GetStatus(), pipeline.StatusDegraded)
			is.True(strings.HasPrefix(pl.Error, "pipeline.settings_references_unsupported: "))
			is.Equal(plugins.calls, 0)
		})
	}
}

type countingPlugins struct{ calls int }

func (c *countingPlugins) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	c.calls++
	return nil, cerrors.New("unexpected plugin dispenser request")
}
