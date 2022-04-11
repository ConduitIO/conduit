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
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
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

	// pluginDispenser is used to dispense the plugin.
	pluginDispenser plugin.Dispenser

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// plugin is the running instance of the destination plugin.
	plugin plugin.DestinationPlugin

	// m can lock a destination from concurrent access (e.g. in connector persister).
	m sync.Mutex
	// wg tracks the number of in flight calls to the plugin.
	wg sync.WaitGroup
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
	s.m.Lock()
	defer s.m.Unlock()
	return s.plugin != nil
}

func (s *destination) Errors() <-chan error {
	return s.errs
}

func (s *destination) Validate(ctx context.Context, settings map[string]string) error {
	dest, err := s.pluginDispenser.DispenseDestination()
	if err != nil {
		return err
	}
	defer func() {
		_ = dest.Teardown(ctx)
	}()

	err = dest.Configure(ctx, settings)
	if err != nil {
		return cerrors.Errorf("invalid destination config: %w", err)
	}
	return nil
}

func (s *destination) Open(ctx context.Context) error {
	// lock destination as we are about to mutate the plugin field
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin != nil {
		return plugin.ErrPluginRunning
	}

	s.logger.Debug(ctx).Msg("starting destination connector plugin")
	dest, err := s.pluginDispenser.DispenseDestination()
	if err != nil {
		return err
	}

	s.logger.Debug(ctx).Msg("configuring destination connector plugin")
	err = dest.Configure(ctx, s.XConfig.Settings)
	if err != nil {
		_ = dest.Teardown(ctx)
		return err
	}

	err = dest.Start(ctx)
	if err != nil {
		_ = dest.Teardown(ctx)
		return err
	}

	s.logger.Info(ctx).Msg("destination connector plugin successfully started")

	s.plugin = dest
	s.persister.ConnectorStarted()
	return nil
}

func (s *destination) Teardown(ctx context.Context) error {
	// lock destination as we are about to mutate the plugin field
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	s.logger.Debug(ctx).Msg("stopping destination connector plugin")
	err := s.plugin.Stop(ctx)

	// wait for any calls to the plugin to stop running first (e.g. Ack or Write)
	s.wg.Wait()

	s.logger.Debug(ctx).Msg("tearing down destination connector plugin")
	err = multierror.Append(err, s.plugin.Teardown(ctx))

	s.plugin = nil
	s.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down plugin: %w", err)
	}

	s.logger.Info(ctx).Msg("connector plugin successfully torn down")
	return nil
}

func (s *destination) Write(ctx context.Context, r record.Record) error {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	err = s.plugin.Write(ctx, r)
	if err != nil {
		return cerrors.Errorf("error writing record: %w", err)
	}

	return nil
}

func (s *destination) Ack(ctx context.Context) (record.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	p, err := s.plugin.Ack(ctx)
	if err != nil {
		return nil, cerrors.Errorf("error receiving ack: %w", err)
	}

	return p, nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (s *destination) preparePluginCall() (func(), error) {
	s.m.Lock()
	defer s.m.Unlock()
	if s.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	s.wg.Add(1)
	return s.wg.Done, nil
}

func (s *destination) Lock() {
	s.m.Lock()
}

func (s *destination) Unlock() {
	s.m.Unlock()
}
