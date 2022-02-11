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

type source struct {
	// exported fields are persisted in the store but must not collide with
	// interface methods, so they are prefixed with X

	XID     string
	XConfig Config
	XState  SourceState

	// logger is used for logging and is set when source is created.
	logger log.CtxLogger

	// persister is used for persisting the connector state when it changes.
	persister *Persister

	// pluginDispenser TODO
	pluginDispenser plugin.Dispenser

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// below fields are nil when source is created and are managed by source
	// internally.
	plugin plugin.SourcePlugin

	// m can lock a source from concurrent access (e.g. in connector persister)
	m sync.Mutex
}

func (s *source) ID() string {
	return s.XID
}

func (s *source) Type() Type {
	return TypeSource
}

func (s *source) Config() Config {
	return s.XConfig
}

func (s *source) SetConfig(d Config) {
	s.XConfig = d
}

func (s *source) State() SourceState {
	return s.XState
}

func (s *source) SetState(state SourceState) {
	s.XState = state
}

func (s *source) IsRunning() bool {
	return s.plugin != nil
}

func (s *source) Errors() <-chan error {
	return s.errs
}

func (s *source) Validate(ctx context.Context, settings map[string]string) error {
	src, err := s.pluginDispenser.DispenseSource()
	if err != nil {
		return err
	}
	defer func() {
		_ = src.Teardown(ctx)
	}()

	err = src.Configure(ctx, settings)
	if err != nil {
		return cerrors.Errorf("invalid plugin config: %w", err)
	}
	return nil
}

func (s *source) Open(ctx context.Context) error {
	if s.IsRunning() {
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

	err = src.Start(ctx, s.XState.Position)
	if err != nil {
		_ = src.Teardown(ctx)
		return err
	}

	s.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.plugin = src
	s.persister.ConnectorStarted()
	return nil
}

func (s *source) Stop(ctx context.Context) error {
	if !s.IsRunning() {
		return plugin.ErrPluginNotRunning
	}

	s.logger.Debug(ctx).Msg("stopping source connector plugin")
	err := s.plugin.Stop(ctx)
	if err != nil {
		return cerrors.Errorf("could not stop plugin: %w", err)
	}

	s.logger.Info(ctx).Msg("connector plugin successfully stopped")
	return nil
}

func (s *source) Teardown(ctx context.Context) error {
	if !s.IsRunning() {
		return plugin.ErrPluginNotRunning
	}

	s.logger.Debug(ctx).Msg("tearing down source connector plugin")

	err := s.plugin.Teardown(ctx)

	s.plugin = nil
	s.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down plugin: %w", err)
	}

	s.logger.Info(ctx).Msg("connector plugin successfully torn down")
	return nil
}

func (s *source) Read(ctx context.Context) (record.Record, error) {
	if !s.IsRunning() {
		return record.Record{}, plugin.ErrPluginNotRunning
	}

	r, err := s.plugin.Read(ctx)
	if err != nil {
		return r, err
	}

	if r.Key == nil {
		r.Key = record.RawData{}
	}
	if r.Payload == nil {
		r.Payload = record.RawData{}
	}
	r.ReadAt = time.Now().UTC() // TODO now that records can be read asynchronously, should we move this to the plugin SDK?
	r.SourceID = s.ID()

	return r, nil
}

func (s *source) Ack(ctx context.Context, p record.Position) error {
	if !s.IsRunning() {
		return plugin.ErrPluginNotRunning
	}

	err := s.plugin.Ack(ctx, p)
	if err != nil {
		return err
	}

	// lock to prevent race condition with connector persister
	s.m.Lock()
	defer s.m.Unlock()

	s.XState.Position = p
	s.persister.Persist(ctx, s, func(err error) {
		if err != nil {
			s.errs <- err
		}
	})
	return nil
}

func (s *source) Lock() {
	s.m.Lock()
}

func (s *source) Unlock() {
	s.m.Unlock()
}
