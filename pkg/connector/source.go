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

type Source struct {
	Instance *Instance

	dispenser plugin.Dispenser
	plugin    plugin.SourcePlugin

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
}

type SourceState struct {
	Position record.Position
}

func (s *Source) ID() string {
	return s.Instance.ID
}

func (s *Source) Errors() <-chan error {
	return s.errs
}

// init dispenses the plugin and configures it.
func (s *Source) initPlugin(ctx context.Context) (plugin.SourcePlugin, error) {
	s.Instance.logger.Debug(ctx).Msg("starting source connector plugin")
	src, err := s.dispenser.DispenseSource()
	if err != nil {
		return nil, err
	}

	s.Instance.logger.Debug(ctx).Msg("configuring source connector plugin")
	err = src.Configure(ctx, s.Instance.Config.Settings)
	if err != nil {
		tdErr := src.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
		})
		return nil, err
	}

	return src, nil
}

func (s *Source) Open(ctx context.Context) error {
	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.Instance.connector != nil {
		// this shouldn't actually happen, it indicates a problem elsewhere
		return cerrors.New("another instance of the connector is already running")
	}

	src, err := s.initPlugin(ctx)
	if err != nil {
		return err
	}

	var state SourceState
	if s.Instance.State != nil {
		state = s.Instance.State.(SourceState)
	}

	streamCtx, cancelStreamCtx := context.WithCancel(ctx)
	err = src.Start(streamCtx, state.Position)
	if err != nil {
		cancelStreamCtx()
		tdErr := src.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
		})
		return err
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.plugin = src
	s.stopStream = cancelStreamCtx
	s.Instance.connector = s
	s.Instance.persister.ConnectorStarted()

	return nil
}

func (s *Source) Stop(ctx context.Context) (record.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	s.Instance.logger.Debug(ctx).Msg("sending stop signal to source connector plugin")
	lastPosition, err := s.plugin.Stop(ctx)
	if err != nil {
		return nil, cerrors.Errorf("could not stop source plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).
		Bytes(log.RecordPositionField, lastPosition).
		Msg("source connector plugin successfully responded to stop signal")
	return lastPosition, nil
}

func (s *Source) Teardown(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	// close stream
	if s.stopStream != nil {
		s.stopStream()
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Read)
	s.wg.Wait()

	s.Instance.logger.Debug(ctx).Msg("tearing down source connector plugin")
	err := s.plugin.Teardown(ctx)

	s.plugin = nil
	s.Instance.connector = nil
	s.Instance.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down source connector plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully torn down")
	return nil
}

func (s *Source) Read(ctx context.Context) (record.Record, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return record.Record{}, err
	}

	r, err := s.plugin.Read(ctx)
	if err != nil {
		return r, err
	}

	if r.Key == nil {
		r.Key = record.RawData{}
	}
	if r.Payload.Before == nil {
		r.Payload.Before = record.RawData{}
	}
	if r.Payload.After == nil {
		r.Payload.After = record.RawData{}
	}

	if r.Metadata == nil {
		r.Metadata = record.Metadata{}
	}
	// source connector ID is added to all records
	r.Metadata.SetConduitSourceConnectorID(s.Instance.ID)

	s.Instance.inspector.Send(ctx, r)
	return r, nil
}

func (s *Source) Ack(ctx context.Context, p record.Position) error {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	err = s.plugin.Ack(ctx, p)
	if err != nil {
		return err
	}

	// lock as we are updating the state and leave it locked so the persister
	// can safely prepare the connector before it stores it
	s.Instance.Lock()
	defer s.Instance.Unlock()
	s.Instance.State = SourceState{Position: p}
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
