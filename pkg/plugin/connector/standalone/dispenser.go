// Copyright © 2024 Meroxa, Inc.
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

package standalone

import (
	"context"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
)

type Dispenser struct {
	logger zerolog.Logger
	path   string
	opts   []client.Option

	dispensed bool
	client    *goplugin.Client
	m         sync.Mutex
}

func NewDispenser(logger zerolog.Logger, path string, opts ...client.Option) (*Dispenser, error) {
	d := Dispenser{
		logger: logger,
		path:   path,
		opts:   opts,
	}

	return &d, nil
}

func (d *Dispenser) initClient() error {
	c, err := client.New(&hcLogger{logger: d.logger}, d.path, d.opts...)
	if err != nil {
		return cerrors.Errorf("could not create go-plugin client: %w", err)
	}

	d.client = c
	return nil
}

func (d *Dispenser) dispense() error {
	d.m.Lock()
	defer d.m.Unlock()

	if d.dispensed {
		return cerrors.New("plugin already dispensed, can't dispense twice")
	}

	err := d.initClient()
	if err != nil {
		return cerrors.Errorf("could not init plugin client: %w", err)
	}

	d.dispensed = true
	return nil
}

func (d *Dispenser) teardown() {
	d.m.Lock()
	defer d.m.Unlock()

	if !d.dispensed {
		// nothing to do here
		return
	}

	d.logger.Debug().Msg("killing plugin client")
	// kill the process
	d.client.Kill()
	d.client = nil
	d.dispensed = false
}

func (d *Dispenser) DispenseSpecifier() (connector.SpecifierPlugin, error) {
	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("specifier")
	if err != nil {
		return nil, err
	}

	specifier, ok := raw.(connector.SpecifierPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense specifier, got type: %T", raw)
	}

	return specifierPluginDispenserSignaller{specifier, d}, nil
}

func (d *Dispenser) DispenseSource(connectorID string) (connector.SourcePlugin, error) {
	if connectorID != "" {
		d.opts = append(d.opts, client.WithEnvVar("CONNECTOR_ID", connectorID))
	}

	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("source")
	if err != nil {
		return nil, err
	}

	source, ok := raw.(connector.SourcePlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense source, got type: %T", raw)
	}

	return sourcePluginDispenserSignaller{source, d}, nil
}

func (d *Dispenser) DispenseDestination(connectorID string) (connector.DestinationPlugin, error) {
	if connectorID != "" {
		d.opts = append(d.opts, client.WithEnvVar("CONNECTOR_ID", connectorID))
	}

	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("destination")
	if err != nil {
		return nil, err
	}

	destination, ok := raw.(connector.DestinationPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense destination, got type: %T", raw)
	}

	return destinationPluginDispenserSignaller{destination, d}, nil
}

type specifierPluginDispenserSignaller struct {
	connector.SpecifierPlugin
	d *Dispenser
}

func (s specifierPluginDispenserSignaller) Specify(ctx context.Context, req pconnector.SpecifierSpecifyRequest) (pconnector.SpecifierSpecifyResponse, error) {
	defer s.d.teardown()
	return s.SpecifierPlugin.Specify(ctx, req)
}

type sourcePluginDispenserSignaller struct {
	connector.SourcePlugin
	d *Dispenser
}

func (s sourcePluginDispenserSignaller) Teardown(ctx context.Context, req pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	defer s.d.teardown()
	return s.SourcePlugin.Teardown(ctx, req)
}

type destinationPluginDispenserSignaller struct {
	connector.DestinationPlugin
	d *Dispenser
}

func (s destinationPluginDispenserSignaller) Teardown(ctx context.Context, req pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	defer s.d.teardown()
	return s.DestinationPlugin.Teardown(ctx, req)
}
