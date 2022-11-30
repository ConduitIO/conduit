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
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
)

type source struct {
	// exported fields are persisted in the store but must not collide with
	// interface methods, so they are prefixed with X

	XID            string
	XConfig        Config
	XState         SourceState
	XProvisionedBy ProvisionType
	// timestamps
	XCreatedAt time.Time
	XUpdatedAt time.Time

	// logger is used for logging and is set when source is created.
	logger log.CtxLogger

	// persister is used for persisting the connector state when it changes.
	persister *Persister

	// pluginDispenser is used to dispense the plugin.
	pluginDispenser plugin.Dispenser

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// plugin is the running instance of the source plugin.
	plugin plugin.SourcePlugin

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// m can lock a source from concurrent access (e.g. in connector persister).
	m sync.Mutex
	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
}

var _ Source = (*source)(nil)

func (s *source) ID() string {
	return s.XID
}

func (s *source) Type() Type {
	return TypeSource
}

func (s *source) Config() Config {
	return s.XConfig
}

func (s *source) SetConfig(c Config) {
	s.XConfig = c
}

func (s *source) ProvisionedBy() ProvisionType {
	return s.XProvisionedBy
}

func (s *source) CreatedAt() time.Time {
	return s.XCreatedAt
}

func (s *source) UpdatedAt() time.Time {
	return s.XUpdatedAt
}

func (s *source) SetUpdatedAt(t time.Time) {
	s.XUpdatedAt = t
}

func (s *source) State() SourceState {
	return s.XState
}

func (s *source) SetState(state SourceState) {
	s.XState = state
}

func (s *source) IsRunning() bool {
	s.m.Lock()
	defer s.m.Unlock()
	return s.plugin != nil
}

func (s *source) Errors() <-chan error {
	return s.errs
}

func (s *source) Inspect(_ context.Context) *inspector.Session {
	// TODO implement me
	panic("implement me")
}

func (s *source) Validate(ctx context.Context, settings map[string]string) (err error) {
	src, err := s.pluginDispenser.DispenseSource()
	if err != nil {
		return err
	}
	defer func() {
		tmpErr := src.Teardown(ctx)
		err = cerrors.LogOrReplace(err, tmpErr, func() {
			s.logger.Err(ctx, tmpErr).Msg("could not teardown source")
		})
	}()

	err = src.Configure(ctx, settings)
	if err != nil {
		return cerrors.Errorf("invalid plugin config: %w", err)
	}
	return nil
}

func (s *source) Open(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin != nil {
		return plugin.ErrPluginRunning
	}

	s.logger.Debug(ctx).Msg("starting source connector plugin")
	src, err := s.pluginDispenser.DispenseSource()
	if err != nil {
		return err
	}

	s.logger.Debug(ctx).Msg("configuring source connector plugin")
	err = src.Configure(ctx, s.XConfig.Settings)
	if err != nil {
		_ = src.Teardown(ctx)
		return err
	}

	streamCtx, cancelStreamCtx := context.WithCancel(ctx)
	err = src.Start(streamCtx, s.XState.Position)
	if err != nil {
		cancelStreamCtx()
		_ = src.Teardown(ctx)
		return err
	}

	s.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.plugin = src
	s.stopStream = cancelStreamCtx
	s.persister.ConnectorStarted()
	return nil
}

func (s *source) Stop(ctx context.Context) (record.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	s.logger.Debug(ctx).Msg("sending stop signal to source connector plugin")
	lastPosition, err := s.plugin.Stop(ctx)
	if err != nil {
		return nil, cerrors.Errorf("could not stop source plugin: %w", err)
	}

	s.logger.Info(ctx).
		Bytes(log.RecordPositionField, lastPosition).
		Msg("source connector plugin successfully responded to stop signal")
	return lastPosition, nil
}

func (s *source) Teardown(ctx context.Context) error {
	// lock source as we are about to mutate the plugin field
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	// close stream
	if s.stopStream != nil {
		s.stopStream()
		s.stopStream = nil
	}

	// wait for any calls to the plugin to stop running first (e.g. Stop, Ack or Read)
	s.wg.Wait()

	s.logger.Debug(ctx).Msg("tearing down source connector plugin")
	err := s.plugin.Teardown(ctx)

	s.plugin = nil
	s.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down source connector plugin: %w", err)
	}

	s.logger.Info(ctx).Msg("source connector plugin successfully torn down")
	return nil
}

func (s *source) Read(ctx context.Context) (record.Record, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return record.Record{}, err
	}

	r, err := s.plugin.Read(ctx)
	if err != nil {
		return r, err
	}

	// TODO rethink if there's an actual benefit in setting these fields
	if r.Key == nil {
		r.Key = record.RawData{}
	}
	if r.Payload.Before == nil {
		r.Payload.Before = record.RawData{}
	}
	if r.Payload.After == nil {
		r.Payload.After = record.RawData{}
	}

	return r, nil
}

func (s *source) Ack(ctx context.Context, p record.Position) error {
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
	s.m.Lock()
	s.XState.Position = p
	s.m.Unlock()

	s.persister.Persist(ctx, s, func(err error) {
		if err != nil {
			s.errs <- err
		}
	})
	return nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (s *source) preparePluginCall() (func(), error) {
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	s.wg.Add(1)
	return s.wg.Done, nil
}

func (s *source) Lock() {
	s.m.Lock()
}

func (s *source) Unlock() {
	s.m.Unlock()
}
