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

package connector

import (
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
)

type Destination struct {
	Instance *Instance

	plugin plugin.DestinationPlugin

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
}

type DestinationState struct {
	Positions map[string]record.Position
}

func (d *Destination) ID() string {
	return d.Instance.ID
}

func (d *Destination) Errors() <-chan error {
	return d.errs
}

// init dispenses the plugin and configures it.
func (d *Destination) initPlugin(ctx context.Context) (plugin.DestinationPlugin, error) {
	d.Instance.logger.Debug(ctx).Msg("starting destination connector plugin")
	dest, err := d.Instance.pluginDispenser.DispenseDestination()
	if err != nil {
		return nil, err
	}

	d.Instance.logger.Debug(ctx).Msg("configuring destination connector plugin")
	err = dest.Configure(ctx, d.Instance.Config.Settings)
	if err != nil {
		_ = dest.Teardown(ctx)
		return nil, err
	}

	return dest, nil
}

func (d *Destination) Open(ctx context.Context) error {
	d.Instance.Lock()
	defer d.Instance.Unlock()
	if d.Instance.connector != nil {
		// this means another connector is running (this shouldn't actually happen)
		return cerrors.New("another instance of the connector is already running")
	}

	dest, err := d.initPlugin(ctx)
	if err != nil {
		return err
	}

	streamCtx, cancelStreamCtx := context.WithCancel(ctx)
	err = dest.Start(streamCtx)
	if err != nil {
		cancelStreamCtx()
		_ = dest.Teardown(ctx)
		return err
	}

	d.Instance.logger.Info(ctx).Msg("destination connector plugin successfully started")

	d.plugin = dest
	d.stopStream = cancelStreamCtx
	d.Instance.connector = d
	d.Instance.persister.ConnectorStarted()

	return nil
}

func (d *Destination) Stop(ctx context.Context, lastPosition record.Position) error {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	d.Instance.logger.Debug(ctx).
		Bytes(log.RecordPositionField, lastPosition).
		Msg("sending stop signal to destination connector plugin")
	err = d.plugin.Stop(ctx, lastPosition)
	if err != nil {
		return cerrors.Errorf("could not stop destination plugin: %w", err)
	}

	d.Instance.logger.Debug(ctx).Msg("destination connector plugin successfully responded to stop signal")
	return nil
}

func (d *Destination) Teardown(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	d.Instance.Lock()
	defer d.Instance.Unlock()
	if d.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	// close stream
	if d.stopStream != nil {
		d.stopStream()
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Write)
	d.wg.Wait()

	d.Instance.logger.Debug(ctx).Msg("tearing down destination connector plugin")
	err := d.plugin.Teardown(ctx)

	d.plugin = nil
	d.Instance.connector = nil
	d.Instance.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down destination connector plugin: %w", err)
	}

	d.Instance.logger.Info(ctx).Msg("destination connector plugin successfully torn down")
	return nil
}

func (d *Destination) Write(ctx context.Context, r record.Record) error {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	d.Instance.inspector.Send(ctx, r)
	err = d.plugin.Write(ctx, r)
	if err != nil {
		return cerrors.Errorf("error writing record: %w", err)
	}

	return nil
}

func (d *Destination) Ack(ctx context.Context) (record.Position, error) {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	return d.plugin.Ack(ctx)
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (d *Destination) preparePluginCall() (func(), error) {
	d.Instance.RLock()
	defer d.Instance.RUnlock()
	if d.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	d.wg.Add(1)
	return d.wg.Done, nil
}
