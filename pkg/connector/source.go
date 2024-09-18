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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

type Source struct {
	Instance *Instance

	dispenser connectorPlugin.Dispenser
	plugin    connectorPlugin.SourcePlugin

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// stream is the stream used to exchange records and acks with the
	// source plugin.
	stream pconnector.SourceRunStreamClient

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// wg tracks the number of in flight calls to the connectorPlugin.
	wg sync.WaitGroup
}

type SourceState struct {
	Position opencdc.Position
}

func (s *Source) ID() string {
	return s.Instance.ID
}

func (s *Source) Errors() <-chan error {
	return s.errs
}

func (s *Source) Open(ctx context.Context) (err error) {
	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.Instance.connector != nil {
		// this shouldn't actually happen, it indicates a problem elsewhere
		return cerrors.New("another instance of the connector is already running")
	}

	s.Instance.logger.Debug(ctx).Msg("dispensing source connector plugin")
	s.plugin, err = s.dispenser.DispenseSource()
	if err != nil {
		return err
	}

	defer func() {
		// ensure the plugin gets torn down if something bad happens
		if err != nil {
			_, tdErr := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})
			if tdErr != nil {
				s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
			}
			s.plugin = nil
		}
	}()

	err = s.configure(ctx)
	if err != nil {
		return err
	}

	lifecycleEventTriggered, err := s.triggerLifecycleEvent(ctx, s.Instance.LastActiveConfig.Settings, s.Instance.Config.Settings)
	if err != nil {
		return err
	}

	if lifecycleEventTriggered {
		// when a lifecycle event is successfully triggered we consider the config active
		s.Instance.LastActiveConfig = s.Instance.Config
		// persist connector in the next batch to store last active config
		err := s.Instance.persister.Persist(ctx, s.Instance, func(err error) {
			if err != nil {
				s.errs <- err
			}
		})
		if err != nil {
			return err
		}
	}

	err = s.open(ctx)
	if err != nil {
		return err
	}

	err = s.run(ctx)
	if err != nil {
		return err
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.Instance.connector = s
	s.Instance.persister.ConnectorStarted()

	return nil
}

func (s *Source) Stop(ctx context.Context) (opencdc.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	s.Instance.logger.Debug(ctx).Msg("sending stop signal to source connector plugin")
	resp, err := s.plugin.Stop(ctx, pconnector.SourceStopRequest{})
	if err != nil {
		return nil, cerrors.Errorf("could not stop source plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).
		Bytes(log.RecordPositionField, resp.LastPosition).
		Msg("source connector plugin successfully responded to stop signal")
	return resp.LastPosition, nil
}

func (s *Source) Teardown(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	s.Instance.logger.Debug(ctx).Msg("closing stream")
	// close stream
	if s.stopStream != nil {
		s.stopStream()
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Read)
	s.wg.Wait()

	s.Instance.logger.Debug(ctx).Msg("tearing down source connector plugin")
	_, err := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})

	s.plugin = nil
	s.Instance.connector = nil
	s.Instance.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down source connector plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully torn down")
	return nil
}

func (s *Source) Read(ctx context.Context) ([]opencdc.Record, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	if s.stream == nil {
		return nil, cerrors.Errorf("source stream not open: %w", connectorPlugin.ErrStreamNotOpen)
	}

	resp, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	for _, r := range resp.Records {
		s.sanitizeRecord(&r)
	}

	s.Instance.inspector.Send(ctx, resp.Records)
	return resp.Records, nil
}

func (s *Source) Ack(ctx context.Context, p []opencdc.Position) error {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	if s.stream == nil {
		return cerrors.Errorf("source stream not open: %w", connectorPlugin.ErrStreamNotOpen)
	}

	err = s.stream.Send(pconnector.SourceRunRequest{AckPositions: p})
	if err != nil {
		return err
	}

	// lock as we are updating the state and leave it locked so the persister
	// can safely prepare the connector before it stores it
	s.Instance.Lock()
	defer s.Instance.Unlock()
	s.Instance.State = SourceState{Position: p[len(p)-1]}
	err = s.Instance.persister.Persist(ctx, s.Instance, func(err error) {
		if err != nil {
			s.errs <- err
		}
	})
	if err != nil {
		return cerrors.Errorf("failed to persist source connector: %w", err)
	}

	return nil
}

func (s *Source) OnDelete(ctx context.Context) (err error) {
	if s.Instance.LastActiveConfig.Settings == nil {
		return nil // the connector was never started, nothing to trigger
	}

	s.Instance.Lock()
	defer s.Instance.Unlock()

	s.Instance.logger.Debug(ctx).Msg("dispensing source connector plugin")
	s.plugin, err = s.dispenser.DispenseSource()
	if err != nil {
		return err
	}

	_, err = s.triggerLifecycleEvent(ctx, s.Instance.LastActiveConfig.Settings, nil)

	// call teardown to close plugin regardless of the error
	_, tdErr := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})

	s.plugin = nil

	err = cerrors.LogOrReplace(err, tdErr, func() {
		s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
	})
	if err != nil {
		return cerrors.Errorf("could not trigger lifecycle event: %w", err)
	}

	return nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (s *Source) preparePluginCall() (func(), error) {
	s.Instance.RLock()
	defer s.Instance.RUnlock()
	if s.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	s.wg.Add(1)
	return s.wg.Done, nil
}

// state returns the SourceState for this connector.
func (s *Source) state() SourceState {
	if s.Instance.State != nil {
		return s.Instance.State.(SourceState)
	}
	return SourceState{}
}

func (s *Source) configure(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("configuring source connector plugin")
	_, err := s.plugin.Configure(ctx, pconnector.SourceConfigureRequest{Config: s.Instance.Config.Settings})
	if err != nil {
		return cerrors.Errorf("could not configure source connector plugin: %w", err)
	}
	return nil
}

func (s *Source) open(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("opening source connector plugin")
	_, err := s.plugin.Open(ctx, pconnector.SourceOpenRequest{
		Position: s.state().Position,
	})
	if err != nil {
		return cerrors.Errorf("could not open source connector plugin: %w", err)
	}
	return nil
}

func (s *Source) run(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("running source connector plugin")
	ctx, stopStream := context.WithCancel(ctx)
	stream := s.plugin.NewStream()
	err := s.plugin.Run(ctx, stream)
	if err != nil {
		stopStream()
		return cerrors.Errorf("could not run source connector plugin: %w", err)
	}
	s.stream = stream.Client()
	s.stopStream = stopStream
	return nil
}

func (s *Source) triggerLifecycleEvent(ctx context.Context, oldConfig, newConfig map[string]string) (ok bool, err error) {
	if s.isEqual(oldConfig, newConfig) {
		return false, nil // nothing to do, last active config is the same as current one
	}

	defer func() {
		if cerrors.Is(err, plugin.ErrUnimplemented) {
			s.Instance.logger.Trace(ctx).Msg("lifecycle events not implemented on source connector plugin (it's probably an older connector)")
			err = nil // ignore error to stay backwards compatible
		}
	}()

	switch {
	// created
	case oldConfig == nil && newConfig != nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"created\" on source connector plugin")
		_, err := s.plugin.LifecycleOnCreated(ctx, pconnector.SourceLifecycleOnCreatedRequest{Config: newConfig})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"created\": %w", err)
		}
		return true, nil

	// updated
	case oldConfig != nil && newConfig != nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"updated\" on source connector plugin")
		_, err := s.plugin.LifecycleOnUpdated(ctx, pconnector.SourceLifecycleOnUpdatedRequest{
			ConfigBefore: oldConfig,
			ConfigAfter:  newConfig,
		})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"updated\": %w", err)
		}
		return true, nil

	// deleted
	case oldConfig != nil && newConfig == nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"deleted\" on source connector plugin")
		_, err := s.plugin.LifecycleOnDeleted(ctx, pconnector.SourceLifecycleOnDeletedRequest{Config: oldConfig})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"deleted\": %w", err)
		}
		return true, nil

	// default should never happen
	default:
		s.Instance.logger.Warn(ctx).
			Any("oldConfig", oldConfig).
			Any("newConfig", newConfig).
			Msg("unexpected combination of old and new config")
		// don't return an error when no event was triggered, strictly speaking
		// the action did not fail
		return false, nil
	}
}

func (s *Source) sanitizeRecord(r *opencdc.Record) {
	if r.Key == nil {
		r.Key = opencdc.RawData{}
	}
	if r.Payload.Before == nil {
		r.Payload.Before = opencdc.RawData{}
	}
	if r.Payload.After == nil {
		r.Payload.After = opencdc.RawData{}
	}

	if r.Metadata == nil {
		r.Metadata = opencdc.Metadata{}
	}
	// source connector ID is added to all records
	r.Metadata.SetConduitSourceConnectorID(s.Instance.ID)
}

func (*Source) isEqual(cfg1, cfg2 map[string]string) bool {
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
