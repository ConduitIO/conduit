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

package builtin

import (
	file "github.com/conduitio/conduit-connector-file"
	generator "github.com/conduitio/conduit-connector-generator"
	kafka "github.com/conduitio/conduit-connector-kafka"
	postgres "github.com/conduitio/conduit-connector-postgres"
	pgdest "github.com/conduitio/conduit-connector-postgres/destination"
	pgsource "github.com/conduitio/conduit-connector-postgres/source"
	s3 "github.com/conduitio/conduit-connector-s3"
	s3destination "github.com/conduitio/conduit-connector-s3/destination"
	s3source "github.com/conduitio/conduit-connector-s3/source"
	"github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	builtinv1 "github.com/conduitio/conduit/pkg/plugin/builtin/v1"
)

var (
	DefaultDispenserFactories = []DispenserFactory{
		sdkDispenserFactory(file.Specification, file.NewSource, file.NewDestination),
		sdkDispenserFactory(kafka.Specification, kafka.NewSource, kafka.NewDestination),
		sdkDispenserFactory(generator.Specification, generator.NewSource, nil),
		sdkDispenserFactory(s3.Specification, s3source.NewSource, s3destination.NewDestination),
		sdkDispenserFactory(postgres.Specification, pgsource.NewSource, pgdest.NewDestination),
	}
)

type Registry struct {
	logger log.CtxLogger

	builders map[string]DispenserFactory
}

type DispenserFactory func(name string, logger log.CtxLogger) plugin.Dispenser

func sdkDispenserFactory(
	specFactory func() sdk.Specification,
	sourceFactory func() sdk.Source,
	destinationFactory func() sdk.Destination,
) DispenserFactory {
	if sourceFactory == nil {
		sourceFactory = func() sdk.Source { return nil }
	}
	if destinationFactory == nil {
		destinationFactory = func() sdk.Destination { return nil }
	}

	return func(name string, logger log.CtxLogger) plugin.Dispenser {
		return builtinv1.NewDispenser(
			name,
			logger,
			sdk.NewSpecifierPlugin(specFactory()),
			sdk.NewSourcePlugin(sourceFactory()),
			sdk.NewDestinationPlugin(destinationFactory()),
		)
	}
}

func NewRegistry(logger log.CtxLogger, factories ...DispenserFactory) *Registry {
	builders := make(map[string]DispenserFactory, len(factories))
	for _, builder := range factories {
		p := builder("", log.CtxLogger{})
		specPlugin, err := p.DispenseSpecifier()
		if err != nil {
			panic(cerrors.Errorf("could not dispense specifier for built in plugin: %w", err))
		}
		specs, err := specPlugin.Specify()
		if err != nil {
			panic(cerrors.Errorf("could not get specs for built in plugin: %w", err))
		}
		if _, ok := builders[specs.Name]; ok {
			panic(cerrors.Errorf("plugin with name %q already registered", specs.Name))
		}
		builders[specs.Name] = builder
	}
	return &Registry{builders: builders, logger: logger.WithComponent("builtin.Registry")}
}

func (r *Registry) New(logger log.CtxLogger, name string) (plugin.Dispenser, error) {
	builder, ok := r.builders[name]
	if !ok {
		return nil, cerrors.Errorf("plugin %q not found", name)
	}
	return builder(name, logger), nil
}

func (r *Registry) List() (map[string]plugin.Specification, error) {
	specs := make(map[string]plugin.Specification)

	for name, dispenser := range r.builders {
		d := dispenser(name, r.logger)
		spec, err := d.DispenseSpecifier()
		if err != nil {
			return nil, cerrors.Errorf("could not dispense specifier for built in plugin: %w", err)
		}
		specs[plugin.BuiltinPluginPrefix+name], err = spec.Specify()
		if err != nil {
			return nil, cerrors.Errorf("could not get specs for built in plugin: %w", err)
		}
	}
	return specs, nil
}
