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
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
)

type Source struct {
	instance  *Instance
	plugin    plugin.SourcePlugin
	inspector *inspector.Inspector

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// m can lock a source from concurrent access (e.g. in connector persister).
	m sync.Mutex
	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
}

func (s *Source) Errors() <-chan error {
	return s.errs
}

func (s *Source) Inspect(ctx context.Context) *inspector.Session {
	return s.inspector.NewSession(ctx)
}

func (s *Source) Open(ctx context.Context) (err error) {
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	defer func() {
		if err != nil {
			_ = s.plugin.Teardown(ctx)
			s.plugin = nil
		}
	}()

	var state SourceState
	if s.instance.State != nil {
		state = s.instance.State.(SourceState)
	}

	s.instance.logger.Debug(ctx).Msg("configuring source connector plugin")
	err = s.plugin.Configure(ctx, s.instance.Config.Settings)
	if err != nil {
		return err
	}

	streamCtx, cancelStreamCtx := context.WithCancel(ctx)
	err = s.plugin.Start(streamCtx, state.Position)
	if err != nil {
		cancelStreamCtx()
		return err
	}

	s.instance.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.stopStream = cancelStreamCtx
	s.instance.persister.ConnectorStarted()
	return nil
}

func (s *Source) Stop(ctx context.Context) (record.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	s.instance.logger.Debug(ctx).Msg("sending stop signal to source connector plugin")
	lastPosition, err := s.plugin.Stop(ctx)
	if err != nil {
		return nil, cerrors.Errorf("could not stop source plugin: %w", err)
	}

	s.instance.logger.Info(ctx).
		Bytes(log.RecordPositionField, lastPosition).
		Msg("source connector plugin successfully responded to stop signal")
	return lastPosition, nil
}

func (s *Source) Teardown(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	// close stream
	if s.stopStream != nil {
		s.stopStream()
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Read)
	s.wg.Wait()

	s.instance.logger.Debug(ctx).Msg("tearing down source connector plugin")
	err := s.plugin.Teardown(ctx)

	s.plugin = nil
	s.instance.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down source connector plugin: %w", err)
	}

	s.instance.logger.Info(ctx).Msg("source connector plugin successfully torn down")
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
	r.Metadata.SetConduitSourceConnectorID(s.instance.ID)

	s.inspector.Send(ctx, r)
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

	// lock to prevent race condition with connector persister
	s.instance.State = SourceState{Position: p}

	s.instance.persister.Persist(ctx, s.instance, func(err error) {
		if err != nil {
			s.errs <- err
		}
	})
	return nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (s *Source) preparePluginCall() (func(), error) {
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	s.wg.Add(1)
	return s.wg.Done, nil
}
