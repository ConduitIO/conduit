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

type destination struct {
	// exported fields are persisted in the store but must not collide with
	// interface methods, so they are prefixed with X

	XID     string
	XConfig Config
	XState  DestinationState

	// logger is used for logging and is set when destination is created.
	logger log.CtxLogger

	// persister is used for persisting the connector state when it changes.
	persister *Persister

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// bellow fields are nil when destination is created and are managed by destination
	// internally.

	client *plugin.Client
	plugin plugins.Destination

	// m can lock a destination from concurrent access (e.g. in connector persister)
	m sync.Mutex
}

func (s *destination) ID() string {
	return s.XID
}

func (s *destination) Type() Type {
	return TypeDestination
}

func (s *destination) Config() Config {
	return s.XConfig
}

func (s *destination) SetConfig(d Config) {
	s.XConfig = d
}

func (s *destination) State() DestinationState {
	return s.XState
}

func (s *destination) SetState(state DestinationState) {
	s.XState = state
}

func (s *destination) IsRunning() bool {
	return s.client != nil
}

func (s *destination) Errors() <-chan error {
	return s.errs
}

func (s *destination) Validate(ctx context.Context, settings map[string]string) error {
	plug := s.plugin

	if !s.IsRunning() {
		client := plugins.NewClient(ctx, s.logger.Logger, s.XConfig.Plugin)
		// start plugin only to validate that the config is valid
		defer client.Kill()

		var err error
		plug, err = plugins.DispenseDestination(client)
		if err != nil {
			return err
		}
	}

	config := plugins.Config{Settings: settings}
	err := plug.Validate(config)
	if err != nil {
		return cerrors.Errorf("invalid destination config: %w", err)
	}

	return nil
}

func (s *destination) Open(ctx context.Context) error {
	if s.IsRunning() {
		return plugins.ErrAlreadyRunning
	}

	s.logger.Debug(ctx).Msg("starting destination connector plugin")
	client := plugins.NewClient(ctx, s.logger.Logger, s.XConfig.Plugin)
	plug, err := plugins.DispenseDestination(client)
	if err != nil {
		client.Kill()
		return err
	}

	s.logger.Debug(ctx).Msg("opening destination connector plugin")
	err = plug.Open(ctx, plugins.Config{Settings: s.XConfig.Settings})
	if err != nil {
		client.Kill()
		return err
	}

	s.logger.Info(ctx).Msg("destination connector plugin successfully started")

	s.client = client
	s.plugin = plug
	s.persister.ConnectorStarted()
	return nil
}

func (s *destination) Teardown(ctx context.Context) error {
	if !s.IsRunning() {
		return plugins.ErrNotRunning
	}

	s.logger.Debug(ctx).Msg("tearing down destination connector plugin")
	err := s.plugin.Teardown()

	// kill client even if teardown fails, we need to stop the child process
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

func (s *destination) Write(ctx context.Context, r record.Record) error {
	if !s.IsRunning() {
		return plugins.ErrNotRunning
	}

	p, err := s.plugin.Write(ctx, r)
	if err != nil {
		return err
	}

	// lock to prevent race condition with connector persister
	s.m.Lock()
	defer s.m.Unlock()

	if s.XState.Positions == nil {
		s.XState.Positions = make(map[string]record.Position)
	}
	s.XState.Positions[r.SourceID] = p
	s.persister.Persist(ctx, s, func(err error) {
		if err != nil {
			s.errs <- err
		}
	})
	return err
}

func (s *destination) Lock() {
	s.m.Lock()
}

func (s *destination) Unlock() {
	s.m.Unlock()
}
