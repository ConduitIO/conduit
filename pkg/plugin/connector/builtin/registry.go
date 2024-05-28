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
	"runtime/debug"

	"github.com/conduitio/conduit-connector-protocol/cplugin"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
)

var (
	// DefaultDispenserFactories contains default dispenser factories for
	// built-in plugins. The key of the map is the import path of the module
	// containing the connector implementation.
	DefaultDispenserFactories = map[string]DispenserFactory{
		// "github.com/conduitio/conduit-connector-file":      NewDispenserFactory(file.Connector),
		// "github.com/conduitio/conduit-connector-kafka":     NewDispenserFactory(kafka.Connector),
		// "github.com/conduitio/conduit-connector-generator": NewDispenserFactory(generator.Connector),
		// "github.com/conduitio/conduit-connector-s3":        NewDispenserFactory(s3.Connector),
		// "github.com/conduitio/conduit-connector-postgres":  NewDispenserFactory(postgres.Connector),
		// "github.com/conduitio/conduit-connector-log":       NewDispenserFactory(connLog.Connector),
	}
)

type Registry struct {
	logger log.CtxLogger

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
}

type blueprint struct {
	fullName         plugin.FullName
	specification    cplugin.Specification
	dispenserFactory DispenserFactory
}

type DispenserFactory func(name plugin.FullName, logger log.CtxLogger) connector.Dispenser

func NewDispenserFactory(conn sdk.Connector) DispenserFactory {
	if conn.NewSource == nil {
		conn.NewSource = func() sdk.Source { return nil }
	}
	if conn.NewDestination == nil {
		conn.NewDestination = func() sdk.Destination { return nil }
	}

	return func(name plugin.FullName, logger log.CtxLogger) connector.Dispenser {
		return NewDispenser(
			name,
			logger,
			func() cplugin.SpecifierPlugin {
				return sdk.NewSpecifierPlugin(conn.NewSpecification(), conn.NewSource(), conn.NewDestination())
			},
			func() cplugin.SourcePlugin { return sdk.NewSourcePlugin(conn.NewSource()) },
			func() cplugin.DestinationPlugin { return sdk.NewDestinationPlugin(conn.NewDestination()) },
		)
	}
}

func NewRegistry(logger log.CtxLogger, factories map[string]DispenserFactory) *Registry {
	logger = logger.WithComponentFromType(Registry{})
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		// we are using modules, build info should always be available, we are staying on the safe side
		logger.Warn(context.Background()).Msg("build info not available, built-in plugin versions may not be read correctly")
		buildInfo = &debug.BuildInfo{} // prevent nil pointer exceptions
	}

	r := &Registry{
		plugins: loadPlugins(buildInfo, factories),
		logger:  logger,
	}
	logger.Info(context.Background()).Int("count", len(r.List())).Msg("builtin plugins initialized")
	return r
}

func loadPlugins(buildInfo *debug.BuildInfo, factories map[string]DispenserFactory) map[string]map[string]blueprint {
	plugins := make(map[string]map[string]blueprint, len(factories))
	for moduleName, factory := range factories {
		specs, err := getSpecification(moduleName, factory, buildInfo)
		if err != nil {
			// stop initialization if a built-in plugin is misbehaving
			panic(err)
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

		latestBp, ok := versionMap[plugin.PluginVersionLatest]
		if !ok || fullName.PluginVersionGreaterThan(latestBp.fullName) {
			versionMap[plugin.PluginVersionLatest] = bp
		}
	}
	return plugins
}

func getSpecification(moduleName string, factory DispenserFactory, buildInfo *debug.BuildInfo) (cplugin.Specification, error) {
	dispenser := factory("", log.CtxLogger{})
	specPlugin, err := dispenser.DispenseSpecifier()
	if err != nil {
		return cplugin.Specification{}, cerrors.Errorf("could not dispense specifier for built in plugin: %w", err)
	}
	resp, err := specPlugin.Specify(context.Background(), cplugin.SpecifierSpecifyRequest{})
	if err != nil {
		return cplugin.Specification{}, cerrors.Errorf("could not get specs for built in plugin: %w", err)
	}

	if version := getModuleVersion(buildInfo.Deps, moduleName); version != "" {
		// overwrite version with the import version
		resp.Specification.Version = version
	}

	return resp.Specification, nil
}

func getModuleVersion(deps []*debug.Module, moduleName string) string {
	for _, dep := range deps {
		if dep.Path == moduleName {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
}

func newFullName(pluginName, pluginVersion string) plugin.FullName {
	return plugin.NewFullName(plugin.PluginTypeBuiltin, pluginName, pluginVersion)
}

func (r *Registry) NewDispenser(logger log.CtxLogger, fullName plugin.FullName) (connector.Dispenser, error) {
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
		return nil, cerrors.Errorf("could not find builtin plugin %q, only found versions %v: %w", fullName, availableVersions, plugin.ErrPluginNotFound)
	}

	return b.dispenserFactory(fullName, logger), nil
}

func (r *Registry) List() map[plugin.FullName]cplugin.Specification {
	specs := make(map[plugin.FullName]cplugin.Specification, len(r.plugins))
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
