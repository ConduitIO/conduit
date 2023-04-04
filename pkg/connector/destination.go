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

	dispenser plugin.Dispenser
	plugin    plugin.DestinationPlugin

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

func (d *Destination) Open(ctx context.Context) (err error) {
	d.Instance.Lock()
	defer d.Instance.Unlock()
	if d.Instance.connector != nil {
		// this shouldn't actually happen, it indicates a problem elsewhere
		return cerrors.New("another instance of the connector is already running")
	}

	d.Instance.logger.Debug(ctx).Msg("dispensing destination connector plugin")
	d.plugin, err = d.dispenser.DispenseDestination()
	if err != nil {
		return err
	}

	defer func() {
		// ensure the plugin gets torn down if something bad happens
		if err != nil {
			tdErr := d.plugin.Teardown(ctx)
			if tdErr != nil {
				d.Instance.logger.Err(ctx, tdErr).Msg("could not tear down destination connector plugin")
			}
			d.plugin = nil
		}
	}()

	err = d.configure(ctx)
	if err != nil {
		return err
	}

	lifecycleEventTriggered, err := d.triggerLifecycleEvent(ctx, d.Instance.LastActiveConfig.Settings, d.Instance.Config.Settings)
	if err != nil {
		return err
	}

	if lifecycleEventTriggered {
		// when a lifecycle event is successfully triggered we consider the config active
		d.Instance.LastActiveConfig = d.Instance.Config
		// persist connector in the next batch to store last active config
		err := d.Instance.persister.Persist(ctx, d.Instance, func(err error) {
			if err != nil {
				d.errs <- err
			}
		})
		if err != nil {
			return err
		}
	}

	err = d.start(ctx)
	if err != nil {
		return err
	}

	d.Instance.logger.Info(ctx).Msg("destination connector plugin successfully started")

	d.Instance.connector = d
	if d.Instance.ProvisionedBy != ProvisionTypeDLQ {
		// DLQ connectors are not persisted
		d.Instance.persister.ConnectorStarted()
	}

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
	// lock destination as we are about to mutate the plugin field
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
	if d.Instance.ProvisionedBy != ProvisionTypeDLQ {
		// DLQ connectors are not persisted
		d.Instance.persister.ConnectorStopped()
	}

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

func (d *Destination) OnDelete(ctx context.Context) (err error) {
	if d.Instance.LastActiveConfig.Settings == nil {
		return nil // the connector was never started, nothing to trigger
	}

	d.Instance.Lock()
	defer d.Instance.Unlock()

	d.Instance.logger.Debug(ctx).Msg("dispensing destination connector plugin")
	d.plugin, err = d.dispenser.DispenseDestination()
	if err != nil {
		return err
	}

	_, err = d.triggerLifecycleEvent(ctx, d.Instance.LastActiveConfig.Settings, nil)

	// call teardown to close plugin regardless of the error
	tdErr := d.plugin.Teardown(ctx)

	d.plugin = nil

	err = cerrors.LogOrReplace(err, tdErr, func() {
		d.Instance.logger.Err(ctx, tdErr).Msg("could not tear down destination connector plugin")
	})
	if err != nil {
		return cerrors.Errorf("could not trigger lifecycle event: %w", err)
	}

	return nil
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

func (d *Destination) configure(ctx context.Context) error {
	d.Instance.logger.Trace(ctx).Msg("configuring destination connector plugin")
	err := d.plugin.Configure(ctx, d.Instance.Config.Settings)
	if err != nil {
		return cerrors.Errorf("could not configure destination connector plugin: %w", err)
	}
	return nil
}

func (d *Destination) triggerLifecycleEvent(ctx context.Context, oldConfig, newConfig map[string]string) (ok bool, err error) {
	if d.isEqual(oldConfig, newConfig) {
		return false, nil // nothing to do, last active config is the same as current one
	}

	defer func() {
		if cerrors.Is(err, plugin.ErrUnimplemented) {
			d.Instance.logger.Trace(ctx).Msg("lifecycle events not implemented on destination connector plugin (it's probably an older connector)")
			err = nil // ignore error to stay backwards compatible
		}
	}()

	switch {
	// created
	case oldConfig == nil && newConfig != nil:
		d.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"created\" on destination connector plugin")
		err := d.plugin.LifecycleOnCreated(ctx, newConfig)
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"created\": %w", err)
		}
		return true, nil

	// updated
	case oldConfig != nil && newConfig != nil:
		d.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"updated\" on destination connector plugin")
		err := d.plugin.LifecycleOnUpdated(ctx, oldConfig, newConfig)
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"updated\": %w", err)
		}
		return true, nil

	// deleted
	case oldConfig != nil && newConfig == nil:
		d.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"deleted\" on destination connector plugin")
		err := d.plugin.LifecycleOnDeleted(ctx, oldConfig)
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"deleted\": %w", err)
		}
		return true, nil

	// default should never happen
	default:
		d.Instance.logger.Warn(ctx).
			Any("oldConfig", oldConfig).
			Any("newConfig", newConfig).
			Msg("unexpected combination of old and new config")
		// don't return an error when no event was triggered, strictly speaking
		// the action did not fail
		return false, nil
	}
}

func (d *Destination) start(ctx context.Context) error {
	d.Instance.logger.Trace(ctx).Msg("starting destination connector plugin")
	ctx, d.stopStream = context.WithCancel(ctx)
	err := d.plugin.Start(ctx)
	if err != nil {
		d.stopStream()
		d.stopStream = nil
		return cerrors.Errorf("could not start destination connector plugin: %w", err)
	}
	return nil
}

func (*Destination) isEqual(cfg1, cfg2 map[string]string) bool {
	if len(cfg1) != len(cfg2) {
		return false
	}
	for k, v := range cfg1 {
		if w, ok := cfg2[k]; !ok || v != w {
			return false
		}
	}
	return (cfg1 != nil) == (cfg2 != nil)
}
