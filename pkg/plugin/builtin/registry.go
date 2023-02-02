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
	"context"

	file "github.com/conduitio/conduit-connector-file"
	generator "github.com/conduitio/conduit-connector-generator"
	kafka "github.com/conduitio/conduit-connector-kafka"
	connLog "github.com/conduitio/conduit-connector-log"
	postgres "github.com/conduitio/conduit-connector-postgres"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	s3 "github.com/conduitio/conduit-connector-s3"
	sdk "github.com/conduitio/conduit-connector-sdk"
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
		sdkDispenserFactory(connLog.Connector),
	}
)

type Registry struct {
	logger    log.CtxLogger
	factories []DispenserFactory

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
}

type blueprint struct {
	fullName         plugin.FullName
	specification    plugin.Specification
	dispenserFactory DispenserFactory
}

type DispenserFactory func(name plugin.FullName, logger log.CtxLogger) plugin.Dispenser

func sdkDispenserFactory(connector sdk.Connector) DispenserFactory {
	if connector.NewSource == nil {
		connector.NewSource = func() sdk.Source { return nil }
	}
	if connector.NewDestination == nil {
		connector.NewDestination = func() sdk.Destination { return nil }
	}

	return func(name plugin.FullName, logger log.CtxLogger) plugin.Dispenser {
		return builtinv1.NewDispenser(
			name,
			logger,
			func() cpluginv1.SpecifierPlugin {
				return sdk.NewSpecifierPlugin(connector.NewSpecification(), connector.NewSource(), connector.NewDestination())
			},
			func() cpluginv1.SourcePlugin { return sdk.NewSourcePlugin(connector.NewSource()) },
			func() cpluginv1.DestinationPlugin { return sdk.NewDestinationPlugin(connector.NewDestination()) },
		)
	}
}

func NewRegistry(logger log.CtxLogger, factories ...DispenserFactory) *Registry {
	r := &Registry{
		logger:    logger.WithComponent("builtin.Registry"),
		factories: factories,
	}
	r.plugins = r.loadPlugins()
	r.logger.Info(context.Background()).Int("count", len(r.List())).Msg("builtin plugins initialized")
	return r
}

func (r *Registry) loadPlugins() map[string]map[string]blueprint {
	plugins := make(map[string]map[string]blueprint, len(r.factories))
	for _, factory := range r.factories {
		dispenser := factory("", log.CtxLogger{})
		specPlugin, err := dispenser.DispenseSpecifier()
		if err != nil {
			panic(cerrors.Errorf("could not dispense specifier for built in plugin: %w", err))
		}
		specs, err := specPlugin.Specify()
		if err != nil {
			panic(cerrors.Errorf("could not get specs for built in plugin: %w", err))
		}

		versionMap := plugins[specs.Name]
		if versionMap == nil {
			versionMap = make(map[string]blueprint)
			plugins[specs.Name] = versionMap
		}

		fullName := newFullName(specs.Name, specs.Version)
		if _, ok := versionMap[specs.Version]; ok {
			panic(cerrors.Errorf("plugin %q already registered", fullName))
		}

		bp := blueprint{
			fullName:         fullName,
			dispenserFactory: factory,
			specification:    specs,
		}
		versionMap[specs.Version] = bp

		latestFullName := versionMap[plugin.PluginVersionLatest].fullName
		if fullName.PluginVersionGreaterThan(latestFullName) {
			versionMap[plugin.PluginVersionLatest] = bp
		}
	}
	return plugins
}

func newFullName(pluginName, pluginVersion string) plugin.FullName {
	return plugin.NewFullName(plugin.PluginTypeBuiltin, pluginName, pluginVersion)
}

func (r *Registry) NewDispenser(logger log.CtxLogger, fullName plugin.FullName) (plugin.Dispenser, error) {
	versionMap, ok := r.plugins[fullName.PluginName()]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	b, ok := versionMap[fullName.PluginVersion()]
	if !ok {
		availableVersions := make([]string, 0, len(versionMap))
		for k := range versionMap {
			availableVersions = append(availableVersions, k)
		}
		return nil, cerrors.Errorf("could not find builtin plugin, only found versions %v: %w", availableVersions, plugin.ErrPluginNotFound)
	}

	return b.dispenserFactory(fullName, logger), nil
}

func (r *Registry) List() map[plugin.FullName]plugin.Specification {
	specs := make(map[plugin.FullName]plugin.Specification, len(r.plugins))
	for _, versions := range r.plugins {
		for version, bp := range versions {
			if version == plugin.PluginVersionLatest {
				continue // skip latest versions
			}
			specs[bp.fullName] = bp.specification
		}
	}
	return specs
}
