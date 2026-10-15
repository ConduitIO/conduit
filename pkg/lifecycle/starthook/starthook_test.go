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

package starthook

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
)

type fakeConnectors map[string]*connector.Instance

func (f fakeConnectors) Get(_ context.Context, id string) (*connector.Instance, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return nil, connector.ErrInstanceNotFound
}

type fakeProcessors struct {
	instances  map[string]*processor.Instance
	standalone map[string]bool
}

func (f fakeProcessors) Get(_ context.Context, id string) (*processor.Instance, error) {
	if p, ok := f.instances[id]; ok {
		return p, nil
	}
	return nil, processor.ErrInstanceNotFound
}

func (f fakeProcessors) ResolvesToStandalone(p string) bool { return f.standalone[p] }

type fakeConnPlugins map[string]bool

func (f fakeConnPlugins) ResolvesToStandalone(p string) bool { return f[p] }

// TestLoad_ExecutionOrder: Load returns sources, pipeline processors and
// destinations in the stored order, with each connector's index in the
// pipeline's connector list, and skips instances that do not exist.
func TestLoad_ExecutionOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	conns := fakeConnectors{
		"d": {ID: "d", Type: connector.TypeDestination, Plugin: kafka, ProcessorIDs: []string{"dp"}},
		"s": {ID: "s", Type: connector.TypeSource, Plugin: pgPlug, ProcessorIDs: []string{"sp1", "missing", "sp2"}},
	}
	procs := fakeProcessors{instances: map[string]*processor.Instance{
		"sp1": {ID: "sp1", Plugin: "a"}, "sp2": {ID: "sp2", Plugin: "b"},
		"pp": {ID: "pp", Plugin: "c"}, "dp": {ID: "dp", Plugin: "d"},
	}}
	pli := &pipeline.Instance{
		ID:            "pl",
		ConnectorIDs:  []string{"d", "gone", "s"},
		ProcessorIDs:  []string{"pp"},
		ProvisionedBy: pipeline.ProvisionTypeAPI,
	}
	topo, err := Load(ctx, pli, Services{Connectors: conns, Processors: procs, ConnectorPlugins: fakeConnPlugins{}})
	is.NoErr(err)
	is.True(!topo.FromConfig)
	is.Equal(len(topo.Sources), 1)
	is.Equal(topo.Sources[0].Index, 2)
	is.Equal(topo.Sources[0].Processors, []Processor{{ID: "sp1", Plugin: "a"}, {ID: "sp2", Plugin: "b"}})
	is.Equal(topo.Processors, []Processor{{ID: "pp", Plugin: "c"}})
	is.Equal(len(topo.Destinations), 1)
	is.Equal(topo.Destinations[0].Index, 0)
}

// TestCheck_UsesBothResolvers: Check asks the processor service about
// processors and the connector plugin service about connectors.
func TestCheck_UsesBothResolvers(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	conns := fakeConnectors{
		"pl:s": {ID: "pl:s", Type: connector.TypeSource, Plugin: "postgres"},
		"pl:d": {ID: "pl:d", Type: connector.TypeDestination, Plugin: kafka, ProcessorIDs: []string{"pl:d:enc"}},
	}
	procs := fakeProcessors{
		instances: map[string]*processor.Instance{"pl:d:enc": {ID: "pl:d:enc", Plugin: "debezium.encode"}},
		// "postgres" as a processor name must not be confused with the
		// connector plugin of the same name.
		standalone: map[string]bool{"postgres": true},
	}
	pli := &pipeline.Instance{ID: "pl", ConnectorIDs: []string{"pl:s", "pl:d"}, ProvisionedBy: pipeline.ProvisionTypeConfig}
	svc := Services{Connectors: conns, Processors: procs, ConnectorPlugins: fakeConnPlugins{}}
	is.NoErr(Check(ctx, pli, svc))

	svc.ConnectorPlugins = fakeConnPlugins{"postgres": true}
	ce := requireRefusal(t, Check(ctx, pli, svc))
	is.Equal(ce.ConfigPath, "/connectors/0")
}
