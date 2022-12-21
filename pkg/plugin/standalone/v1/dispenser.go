// Copyright © 2022 Meroxa, Inc.
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

package standalonev1

import (
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
)

type Dispenser struct {
	logger zerolog.Logger
	path   string
	opts   []ClientOption

	dispensed bool
	client    *goplugin.Client
	m         sync.Mutex
}

func NewDispenser(logger zerolog.Logger, path string, opts ...ClientOption) (*Dispenser, error) {
	d := Dispenser{
		logger: logger,
		path:   path,
		opts:   opts,
	}

	err := d.initClient()
	if err != nil {
		return nil, err
	}

	return &d, nil
}

func (d *Dispenser) initClient() error {
	c, err := NewClient(d.logger, d.path, d.opts...)
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
		return err
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

func (d *Dispenser) DispenseSpecifier() (plugin.SpecifierPlugin, error) {
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

	specifier, ok := raw.(plugin.SpecifierPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense specifier, got type: %T", raw)
	}

	return specifierPluginDispenserSignaller{specifier, d}, nil
}

func (d *Dispenser) DispenseSource() (plugin.SourcePlugin, error) {
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

	source, ok := raw.(plugin.SourcePlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense source, got type: %T", raw)
	}

	return sourcePluginDispenserSignaller{source, d}, nil
}

func (d *Dispenser) DispenseDestination() (plugin.DestinationPlugin, error) {
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

	destination, ok := raw.(plugin.DestinationPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense destination, got type: %T", raw)
	}

	return destinationPluginDispenserSignaller{destination, d}, nil
}

type specifierPluginDispenserSignaller struct {
	plugin.SpecifierPlugin
	d *Dispenser
}

func (s specifierPluginDispenserSignaller) Specify() (plugin.Specification, error) {
	defer s.d.teardown()
	return s.SpecifierPlugin.Specify()
}

type sourcePluginDispenserSignaller struct {
	plugin.SourcePlugin
	d *Dispenser
}

func (s sourcePluginDispenserSignaller) Teardown(ctx context.Context) error {
	defer s.d.teardown()
	return s.SourcePlugin.Teardown(ctx)
}

type destinationPluginDispenserSignaller struct {
	plugin.DestinationPlugin
	d *Dispenser
}

func (s destinationPluginDispenserSignaller) Teardown(ctx context.Context) error {
	defer s.d.teardown()
	return s.DestinationPlugin.Teardown(ctx)
}
