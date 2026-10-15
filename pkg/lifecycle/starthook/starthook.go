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
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

// ConnectorService fetches stored connector instances.
type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
}

// ProcessorService fetches stored processor instances and reports whether a
// processor plugin reference resolves to a standalone plugin.
type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	ResolvesToStandalone(pluginName string) bool
}

// ConnectorPluginService reports whether a connector plugin reference
// resolves to a standalone plugin.
type ConnectorPluginService interface {
	ResolvesToStandalone(name string) bool
}

// Services are what Check reads. Each engine passes its own services.
type Services struct {
	Connectors       ConnectorService
	Processors       ProcessorService
	ConnectorPlugins ConnectorPluginService
}

// Topology is a pipeline as records flow through it: each source and its
// processors, then the pipeline processors, then each destination and its
// processors. Order within each list is execution order.
type Topology struct {
	PipelineID string
	// FromConfig is true when the pipeline was provisioned from a config
	// document, so a JSON pointer into that document addresses something.
	FromConfig   bool
	Sources      []Connector
	Processors   []Processor
	Destinations []Connector
}

// Connector is one connector of a Topology.
type Connector struct {
	ID     string
	Plugin string
	// Index is the connector's position in the pipeline's connector list,
	// which is its index in the config document's connectors.
	Index      int
	Processors []Processor
}

// Processor is one processor of a Topology.
type Processor struct {
	ID     string
	Plugin string
}

// Resolver reports whether a plugin reference runs as a standalone plugin.
type Resolver struct {
	Processor func(plugin string) bool
	Connector func(plugin string) bool
}

// Check runs every start check against the stored pipeline pl and returns the
// first refusal. Connectors and processors that cannot be found are skipped:
// the engine's build fetches the same instances and reports the real error.
func Check(ctx context.Context, pl *pipeline.Instance, svc Services) error {
	t, err := Load(ctx, pl, svc)
	if err != nil {
		return err
	}
	r := Resolver{
		Processor: svc.Processors.ResolvesToStandalone,
		Connector: svc.ConnectorPlugins.ResolvesToStandalone,
	}
	return CheckTopology(t, r)
}

// CheckTopology runs every start check against t.
func CheckTopology(t Topology, r Resolver) error {
	return checkStandaloneBeforeEncode(t, r)
}

// Load reads pl's topology from the stored instances.
func Load(ctx context.Context, pl *pipeline.Instance, svc Services) (Topology, error) {
	t := Topology{
		PipelineID: pl.ID,
		FromConfig: pl.ProvisionedBy != pipeline.ProvisionTypeAPI,
	}
	for i, id := range pl.ConnectorIDs {
		inst, err := svc.Connectors.Get(ctx, id)
		if err != nil {
			if cerrors.Is(err, connector.ErrInstanceNotFound) {
				continue
			}
			return Topology{}, cerrors.Errorf("could not fetch connector %s: %w", id, err)
		}
		inst.RLock()
		c := Connector{ID: inst.ID, Plugin: inst.Plugin, Index: i}
		typ := inst.Type
		procIDs := append([]string(nil), inst.ProcessorIDs...)
		inst.RUnlock()

		if c.Processors, err = loadProcessors(ctx, procIDs, svc.Processors); err != nil {
			return Topology{}, err
		}
		switch typ {
		case connector.TypeSource:
			t.Sources = append(t.Sources, c)
		case connector.TypeDestination:
			t.Destinations = append(t.Destinations, c)
		}
	}
	var err error
	if t.Processors, err = loadProcessors(ctx, pl.ProcessorIDs, svc.Processors); err != nil {
		return Topology{}, err
	}
	return t, nil
}

func loadProcessors(ctx context.Context, ids []string, svc ProcessorService) ([]Processor, error) {
	var out []Processor
	for _, id := range ids {
		inst, err := svc.Get(ctx, id)
		if err != nil {
			if cerrors.Is(err, processor.ErrInstanceNotFound) {
				continue
			}
			return nil, cerrors.Errorf("could not fetch processor %s: %w", id, err)
		}
		out = append(out, Processor{ID: inst.ID, Plugin: inst.Plugin})
	}
	return out, nil
}

// userID strips the prefix the provisioner adds to config-defined IDs
// ("<pipeline>:" for connectors and pipeline processors, "<pipeline>:<connector>:"
// for connector processors), giving the ID as written in the config document.
// API-created instances have no such prefix and keep their ID.
func userID(t Topology, parent, id string) string {
	if parent != "" {
		if s, ok := strings.CutPrefix(id, parent+":"); ok {
			return s
		}
	}
	return strings.TrimPrefix(id, t.PipelineID+":")
}

func processorPointer(i int) string { return fmt.Sprintf("/processors/%d", i) }

func connectorPointer(i int) string { return fmt.Sprintf("/connectors/%d", i) }

func connectorProcessorPointer(conn, i int) string {
	return fmt.Sprintf("/connectors/%d/processors/%d", conn, i)
}
