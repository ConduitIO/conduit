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
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hashicorp/go-plugin"
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

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// bellow fields are nil when source is created and are managed by source
	// internally.

	client *plugin.Client
	plugin plugins.Source

	// lastPosition tracks the position for Read calls, the position in XState
	// is only updated on Ack to have strong delivery guarantees between
	// restarts.
	lastPosition record.Position

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
	return s.client != nil
}

func (s *source) Errors() <-chan error {
	return s.errs
}

func (s *source) Validate(ctx context.Context, settings map[string]string) error {
	plug := s.plugin

	if !s.IsRunning() {
		client := plugins.NewClient(ctx, s.logger.Logger, s.XConfig.Plugin)
		// start plugin only to validate that the config is valid
		defer client.Kill()

		var err error
		plug, err = plugins.DispenseSource(client)
		if err != nil {
			return err
		}
	}

	config := plugins.Config{Settings: settings}
	err := plug.Validate(config)
	if err != nil {
		return cerrors.Errorf("invalid source config: %w", err)
	}

	return nil
}

func (s *source) Open(ctx context.Context) error {
	if s.IsRunning() {
		return plugins.ErrAlreadyRunning
	}

	s.logger.Debug(ctx).Msg("starting source connector plugin")
	client := plugins.NewClient(ctx, s.logger.Logger, s.XConfig.Plugin)
	plug, err := plugins.DispenseSource(client)
	if err != nil {
		client.Kill()
		return err
	}

	s.logger.Debug(ctx).Msg("opening source connector plugin")
	err = plug.Open(ctx, plugins.Config{Settings: s.XConfig.Settings})
	if err != nil {
		errTd := plug.Teardown()
		if errTd != nil {
			s.logger.Err(ctx, errTd).Msg("could not tear down plugin")
		}
		client.Kill()
		return err
	}

	s.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.lastPosition = s.XState.Position
	s.client = client
	s.plugin = plug
	s.persister.ConnectorStarted()
	return nil
}

func (s *source) Teardown(ctx context.Context) error {
	if !s.IsRunning() {
		return plugins.ErrNotRunning
	}

	s.logger.Debug(ctx).Msg("tearing down source connector plugin")
	err := s.plugin.Teardown()

	// kill client even if teardown fails to stop child process
	s.client.Kill()
	s.client = nil
	s.plugin = nil
	s.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not teardown plugin: %w", err)
	}

	s.logger.Info(ctx).Msg("connector plugin successfully torn down")
	return nil
}

func (s *source) Read(ctx context.Context) (record.Record, error) {
	if !s.IsRunning() {
		return record.Record{}, plugins.ErrNotRunning
	}

	r, err := s.plugin.Read(ctx, s.lastPosition)
	if err != nil {
		return r, err
	}
	s.lastPosition = r.Position
	return r, nil
}

func (s *source) Ack(ctx context.Context, p record.Position) error {
	if !s.IsRunning() {
		return plugins.ErrNotRunning
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
