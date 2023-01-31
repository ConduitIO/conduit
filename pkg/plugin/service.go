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

package plugin

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// registry is an object that can create new plugin dispensers. We need to use
// an interface to prevent a cyclic dependency between the plugin package and
// builtin and standalone packages.
// There are two registries that implement this interface:
//   - The built-in registry creates a dispenser which dispenses a plugin adapter
//     that communicates with the plugin directly as if it was a library. These
//     plugins are baked into the Conduit binary and included at compile time.
//   - The standalone registry creates a dispenser which starts the plugin in a
//     separate process and communicates with it via gRPC. These plugins are
//     compiled independently of Conduit and can be included at runtime.
type registry interface {
	NewDispenser(logger log.CtxLogger, name FullName) (Dispenser, error)
	List() map[FullName]Specification
}

type Service struct {
	logger log.CtxLogger

	builtin    registry
	standalone registry
}

func NewService(logger log.CtxLogger, builtin registry, standalone registry) *Service {
	return &Service{
		logger:     logger.WithComponent("plugin.Service"),
		builtin:    builtin,
		standalone: standalone,
	}
}

func (s *Service) Check(_ context.Context) error {
	return nil
}

func (r *Service) NewDispenser(logger log.CtxLogger, name string) (Dispenser, error) {
	logger = logger.WithComponent("plugin")

	fullName := FullName(name)
	switch fullName.PluginType() {
	case PluginTypeStandalone:
		return r.standalone.NewDispenser(logger, fullName)
	case PluginTypeBuiltin:
		return r.builtin.NewDispenser(logger, fullName)
	case PluginTypeAny:
		d, err := r.standalone.NewDispenser(logger, fullName)
		if err != nil {
			r.logger.Debug(context.Background()).Err(err).Msg("could not find standalone plugin dispenser, falling back to builtin plugin")
			d, err = r.builtin.NewDispenser(logger, fullName)
		}
		return d, err
	default:
		return nil, cerrors.Errorf("invalid plugin name prefix %q", fullName.PluginType())
	}
}

func (r *Service) List(ctx context.Context) (map[string]Specification, error) {
	builtinSpecs := r.builtin.List()
	standaloneSpecs := r.standalone.List()

	specs := make(map[string]Specification, len(builtinSpecs)+len(standaloneSpecs))
	for k, v := range builtinSpecs {
		specs[string(k)] = v
	}
	for k, v := range standaloneSpecs {
		specs[string(k)] = v
	}

	return specs, nil
}

func (r *Service) ValidateSourceConfig(ctx context.Context, d Dispenser, settings map[string]string) (err error) {
	src, err := d.DispenseSource()
	if err != nil {
		return cerrors.Errorf("could not dispense source: %w", err)
	}

	defer func() {
		terr := src.Teardown(ctx)
		if err == nil {
			err = terr // only overwrite error if it's nil
		}
	}()

	err = src.Configure(ctx, settings)
	if err != nil {
		return &ValidationError{err: err}
	}

	return nil
}

func (r *Service) ValidateDestinationConfig(ctx context.Context, d Dispenser, settings map[string]string) (err error) {
	dest, err := d.DispenseDestination()
	if err != nil {
		return cerrors.Errorf("could not dispense destination: %w", err)
	}

	defer func() {
		terr := dest.Teardown(ctx)
		if err == nil {
			err = terr // only overwrite error if it's nil
		}
	}()

	err = dest.Configure(ctx, settings)
	if err != nil {
		return &ValidationError{err: err}
	}

	return nil
}
