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

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
)

// registry is an object that can create new plugin dispensers. We need to use
// an interface to prevent a cyclic dependency between the plugin package and
// builtin and standalone packages.
// There are two registries that implement this interface: builtinReg and standaloneReg
type registry interface {
	NewDispenser(logger log.CtxLogger, name plugin.FullName, cfg pconnector.PluginConfig) (Dispenser, error)
	List() map[plugin.FullName]pconnector.Specification
}

// The built-in registry creates a dispenser which dispenses a plugin adapter
// that communicates with the plugin directly as if it was a library. These
// plugins are baked into the Conduit binary and included at compile time.
type builtinReg interface {
	registry

	Init(context.Context)
}

// standaloneReg creates a dispenser which starts the plugin in a
// separate process and communicates with it via gRPC. These plugins are
// compiled independently of Conduit and can be included at runtime.
type standaloneReg interface {
	registry

	Init(ctx context.Context, connUtilsAddr string)
}

type PluginService struct {
	logger log.CtxLogger

	builtinReg    builtinReg
	standaloneReg standaloneReg
	tokenService  *connutils.AuthManager
}

func NewPluginService(
	logger log.CtxLogger,
	builtin builtinReg,
	standalone standaloneReg,
	tokenService *connutils.AuthManager,
) *PluginService {
	return &PluginService{
		logger:        logger.WithComponent("connector.PluginService"),
		builtinReg:    builtin,
		standaloneReg: standalone,
		tokenService:  tokenService,
	}
}

func (s *PluginService) Init(ctx context.Context, connUtilsAddr string) {
	s.builtinReg.Init(ctx)
	s.standaloneReg.Init(ctx, connUtilsAddr)
}

func (s *PluginService) Check(context.Context) error {
	return nil
}

func (s *PluginService) NewDispenser(logger log.CtxLogger, name string, connectorID string) (Dispenser, error) {
	logger = logger.WithComponent("plugin")

	cfg := pconnector.PluginConfig{
		Token:       s.tokenService.GenerateNew(connectorID),
		ConnectorID: connectorID,
		LogLevel:    logger.GetLevel().String(),
	}

	dispenser, err := s.newDispenser(logger, name, cfg)
	if err != nil {
		return nil, err
	}

	return DispenserWithCleanup(
		dispenser,
		func() { s.tokenService.Deregister(cfg.Token) },
	), nil
}

func (s *PluginService) List(context.Context) (map[string]pconnector.Specification, error) {
	builtinSpecs := s.builtinReg.List()
	standaloneSpecs := s.standaloneReg.List()

	specs := make(map[string]pconnector.Specification, len(builtinSpecs)+len(standaloneSpecs))
	for k, v := range builtinSpecs {
		specs[string(k)] = v
	}
	for k, v := range standaloneSpecs {
		specs[string(k)] = v
	}

	return specs, nil
}

func (s *PluginService) ValidateSourceConfig(ctx context.Context, name string, settings map[string]string) (err error) {
	d, err := s.NewDispenser(s.logger, name, "validate-source-config")
	if err != nil {
		return cerrors.Errorf("couldn't get dispenser: %w", err)
	}

	src, err := d.DispenseSource()
	if err != nil {
		return cerrors.Errorf("could not dispense source: %w", err)
	}

	defer func() {
		_, terr := src.Teardown(ctx, pconnector.SourceTeardownRequest{})
		if err == nil {
			err = terr // only overwrite error if it's nil
		}
	}()

	_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: settings})
	if err != nil {
		return &ValidationError{Err: err}
	}

	return nil
}

func (s *PluginService) ValidateDestinationConfig(ctx context.Context, name string, settings map[string]string) (err error) {
	d, err := s.NewDispenser(s.logger, name, "validate-destination-config")
	if err != nil {
		return cerrors.Errorf("couldn't get dispenser: %w", err)
	}

	dest, err := d.DispenseDestination()
	if err != nil {
		return cerrors.Errorf("could not dispense destination: %w", err)
	}

	defer func() {
		_, terr := dest.Teardown(ctx, pconnector.DestinationTeardownRequest{})
		if err == nil {
			err = terr // only overwrite error if it's nil
		}
	}()

	_, err = dest.Configure(ctx, pconnector.DestinationConfigureRequest{Config: settings})
	if err != nil {
		return &ValidationError{Err: err}
	}

	return nil
}

func (s *PluginService) newDispenser(logger log.CtxLogger, name string, cfg pconnector.PluginConfig) (Dispenser, error) {
	fullName := plugin.FullName(name)
	switch fullName.PluginType() {
	case plugin.PluginTypeStandalone:
		return s.standaloneReg.NewDispenser(logger, fullName, cfg)
	case plugin.PluginTypeBuiltin:
		return s.builtinReg.NewDispenser(logger, fullName, cfg)
	case plugin.PluginTypeAny:
		d, err := s.standaloneReg.NewDispenser(logger, fullName, cfg)
		if err != nil {
			s.logger.Debug(context.Background()).Err(err).Msg("could not find standalone plugin dispenser, falling back to builtin plugin")
			d, err = s.builtinReg.NewDispenser(logger, fullName, cfg)
		}
		return d, err
	default:
		return nil, cerrors.Errorf("invalid plugin name prefix %q", fullName.PluginType())
	}
}
