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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
)

type destination struct {
	// exported fields are persisted in the store but must not collide with
	// interface methods, so they are prefixed with X

	XID            string
	XConfig        Config
	XState         DestinationState
	XProvisionedBy ProvisionType

	// logger is used for logging and is set when destination is created.
	logger log.CtxLogger

	// timestamps
	XCreatedAt time.Time
	XUpdatedAt time.Time

	// persister is used for persisting the connector state when it changes.
	persister *Persister

	// pluginDispenser is used to dispense the plugin.
	pluginDispenser plugin.Dispenser

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// plugin is the running instance of the destination plugin.
	plugin plugin.DestinationPlugin

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// m can lock a destination from concurrent access (e.g. in connector persister).
	m sync.Mutex
	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
}

var _ Destination = (*destination)(nil)

func (d *destination) ID() string {
	return d.XID
}

func (d *destination) Type() Type {
	return TypeDestination
}

func (d *destination) Config() Config {
	return d.XConfig
}

func (d *destination) SetConfig(c Config) {
	d.XConfig = c
}

func (d *destination) ProvisionedBy() ProvisionType {
	return d.XProvisionedBy
}

func (d *destination) SetProvisionedBy(p ProvisionType) {
	d.XProvisionedBy = p
}

func (d *destination) CreatedAt() time.Time {
	return d.XCreatedAt
}

func (d *destination) UpdatedAt() time.Time {
	return d.XUpdatedAt
}

func (d *destination) SetUpdatedAt(t time.Time) {
	d.XUpdatedAt = t
}

func (d *destination) State() DestinationState {
	return d.XState
}

func (d *destination) SetState(state DestinationState) {
	d.XState = state
}

func (d *destination) IsRunning() bool {
	d.m.Lock()
	defer d.m.Unlock()
	return d.plugin != nil
}

func (d *destination) Errors() <-chan error {
	return d.errs
}

func (d *destination) Validate(ctx context.Context, settings map[string]string) (err error) {
	dest, err := d.pluginDispenser.DispenseDestination()
	if err != nil {
		return err
	}
	defer func() {
		tmpErr := dest.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tmpErr, func() {
			d.logger.Err(ctx, tmpErr).Msg("could not teardown destination")
		})
	}()

	err = dest.Configure(ctx, settings)
	if err != nil {
		return cerrors.Errorf("invalid destination config: %w", err)
	}
	return nil
}

func (d *destination) Open(ctx context.Context) error {
	// lock destination as we are about to mutate the plugin field
	d.m.Lock()
	defer d.m.Unlock()
	if d.plugin != nil {
		return plugin.ErrPluginRunning
	}

	d.logger.Debug(ctx).Msg("starting destination connector plugin")
	dest, err := d.pluginDispenser.DispenseDestination()
	if err != nil {
		return err
	}

	d.logger.Debug(ctx).Msg("configuring destination connector plugin")
	err = dest.Configure(ctx, d.XConfig.Settings)
	if err != nil {
		_ = dest.Teardown(ctx)
		return err
	}

	streamCtx, cancelStreamCtx := context.WithCancel(ctx)
	err = dest.Start(streamCtx)
	if err != nil {
		cancelStreamCtx()
		_ = dest.Teardown(ctx)
		return err
	}

	d.logger.Info(ctx).Msg("destination connector plugin successfully started")

	d.plugin = dest
	d.stopStream = cancelStreamCtx
	d.persister.ConnectorStarted()
	return nil
}

func (d *destination) Stop(ctx context.Context, lastPosition record.Position) error {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	d.logger.Debug(ctx).
		Bytes(log.RecordPositionField, lastPosition).
		Msg("sending stop signal to destination connector plugin")
	err = d.plugin.Stop(ctx, lastPosition)
	if err != nil {
		return cerrors.Errorf("could not stop destination plugin: %w", err)
	}

	d.logger.Debug(ctx).Msg("destination connector plugin successfully responded to stop signal")
	return nil
}

func (d *destination) Teardown(ctx context.Context) error {
	// lock destination as we are about to mutate the plugin field
	d.m.Lock()
	defer d.m.Unlock()
	if d.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	// close stream
	if d.stopStream != nil {
		d.stopStream()
		d.stopStream = nil
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Write)
	d.wg.Wait()

	d.logger.Debug(ctx).Msg("tearing down destination connector plugin")
	err := d.plugin.Teardown(ctx)
	d.plugin = nil
	d.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down destination connector plugin: %w", err)
	}

	d.logger.Info(ctx).Msg("destination connector plugin successfully torn down")
	return nil
}

func (d *destination) Write(ctx context.Context, r record.Record) error {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	err = d.plugin.Write(ctx, r)
	if err != nil {
		return cerrors.Errorf("error writing record: %w", err)
	}

	return nil
}

func (d *destination) Ack(ctx context.Context) (record.Position, error) {
	cleanup, err := d.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	p, err := d.plugin.Ack(ctx)
	if err != nil {
		return nil, cerrors.Errorf("error receiving ack: %w", err)
	}

	return p, nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (d *destination) preparePluginCall() (func(), error) {
	d.m.Lock()
	defer d.m.Unlock()
	if d.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	d.wg.Add(1)
	return d.wg.Done, nil
}

func (d *destination) Lock() {
	d.m.Lock()
}

func (d *destination) Unlock() {
	d.m.Unlock()
}
