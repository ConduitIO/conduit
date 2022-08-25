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
	"fmt"

	file "github.com/conduitio/conduit-connector-file"
	generator "github.com/conduitio/conduit-connector-generator"
	kafka "github.com/conduitio/conduit-connector-kafka"
	postgres "github.com/conduitio/conduit-connector-postgres"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	s3 "github.com/conduitio/conduit-connector-s3"
	"github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	builtinv1 "github.com/conduitio/conduit/pkg/plugin/builtin/v1"
)

var (
	DefaultDispenserFactories = []DispenserFactory{
		sdkDispenserFactory(file.Connector),
		sdkDispenserFactory(kafka.Connector),
		sdkDispenserFactory(generator.Connector),
		sdkDispenserFactory(s3.Connector),
		sdkDispenserFactory(postgres.Connector),
	}
)

type Registry struct {
	logger log.CtxLogger

	builders map[string]DispenserFactory
	specs    map[string]plugin.Specification
}

type DispenserFactory func(name string, logger log.CtxLogger) plugin.Dispenser

func sdkDispenserFactory(connector sdk.Connector) DispenserFactory {
	if connector.NewSource == nil {
		connector.NewSource = func() sdk.Source { return nil }
	}
	if connector.NewDestination == nil {
		connector.NewDestination = func() sdk.Destination { return nil }
	}

	return func(name string, logger log.CtxLogger) plugin.Dispenser {
		return builtinv1.NewDispenser(
			name,
			logger,
			func() cpluginv1.SpecifierPlugin { return sdk.NewSpecifierPlugin(connector.NewSpecification()) },
			func() cpluginv1.SourcePlugin { return sdk.NewSourcePlugin(connector.NewSource()) },
			func() cpluginv1.DestinationPlugin { return sdk.NewDestinationPlugin(connector.NewDestination()) },
		)
	}
}

func NewRegistry(logger log.CtxLogger, factories ...DispenserFactory) *Registry {
	builders := make(map[string]DispenserFactory, len(factories))
	specs := make(map[string]plugin.Specification)
	for _, builder := range factories {
		p := builder("", log.CtxLogger{})
		specPlugin, err := p.DispenseSpecifier()
		if err != nil {
			panic(cerrors.Errorf("could not dispense specifier for built in plugin: %w", err))
		}
		s, err := specPlugin.Specify()
		if err != nil {
			panic(cerrors.Errorf("could not get specs for built in plugin: %w", err))
		}

		fullName := fmt.Sprintf("%v@%v", s.Name, s.Version)
		if _, ok := builders[fullName]; ok {
			panic(cerrors.Errorf("plugin with name %q already registered", fullName))
		}

		builders[fullName] = builder
		specs[fullName] = s
	}
	return &Registry{
		logger:   logger.WithComponent("builtin.Registry"),
		builders: builders,
		specs:    specs,
	}
}

func (r *Registry) NewDispenser(logger log.CtxLogger, name string) (plugin.Dispenser, error) {
	builder, ok := r.builders[name]
	if !ok {
		return nil, cerrors.Errorf("plugin %q not found", name)
	}
	return builder(name, logger), nil
}

func (r *Registry) List() (map[string]plugin.Specification, error) {
	// copy specs map so it can be freely mutated by the caller
	specs := make(map[string]plugin.Specification, len(r.specs))
	for k, v := range r.specs {
		specs[k] = v
	}
	return specs, nil
}
