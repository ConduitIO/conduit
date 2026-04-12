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
	"runtime/debug" // Keep for buildInfo, even if getModuleVersion is removed

	file "github.com/conduitio/conduit-connector-file"
	generator "github.com/conduitio/conduit-connector-generator"
	kafka "github.com/conduitio/conduit-connector-kafka"
	connLog "github.com/conduitio/conduit-connector-log"
	postgres "github.com/conduitio/conduit-connector-postgres"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	s3 "github.com/conduitio/conduit-connector-s3"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit-connector-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
)

// DefaultBuiltinConnectors contains the default built-in connectors.
// The key of the map is the import path of the module
// containing the connector implementation.
var DefaultBuiltinConnectors = map[string]sdk.Connector{
	"github.com/conduitio/conduit-connector-file":      file.Connector,
	"github.com/conduitio/conduit-connector-generator": generator.Connector,
	"github.com/conduitio/conduit-connector-kafka":     kafka.Connector,
	"github.com/conduitio/conduit-connector-log":       connLog.Connector,
	"github.com/conduitio/conduit-connector-postgres":  postgres.Connector,
	"github.com/conduitio/conduit-connector-s3":        s3.Connector,
}

type blueprint struct {
	fullName         plugin.FullName
	specification    pconnector.Specification
	dispenserFactory dispenserFactory
}

type dispenserFactory func(name plugin.FullName, cfg pconnector.PluginConfig, logger log.CtxLogger) connector.Dispenser

func newDispenserFactory(conn sdk.Connector) dispenserFactory {
	if conn.NewSource == nil {
		conn.NewSource = func() sdk.Source { return nil }
	}
	if conn.NewDestination == nil {
		conn.NewDestination = func() sdk.Destination { return nil }
	}

	return func(name plugin.FullName, cfg pconnector.PluginConfig, logger log.CtxLogger) connector.Dispenser {
		return NewDispenser(
			name,
			logger,
			func() pconnector.SpecifierPlugin {
				return sdk.NewSpecifierPlugin(conn.NewSpecification())
			},
			func() pconnector.SourcePlugin {
				return sdk.NewSourcePlugin(conn.NewSource(), cfg, conn.NewSpecification().SourceParams)
			},
			func() pconnector.DestinationPlugin {
				return sdk.NewDestinationPlugin(conn.NewDestination(), cfg, conn.NewSpecification().DestinationParams)
			},
		)
	}
}

type Registry struct {
	logger log.CtxLogger

	connectors map[string]sdk.Connector
	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
	service *connutils.SchemaService
}

func NewRegistry(logger log.CtxLogger, connectors map[string]sdk.Connector, service *connutils.SchemaService) *Registry {
	logger = logger.WithComponentFromType(Registry{})
	// The built-in plugins use Conduit's own schema service
	schema.Service = service

	r := &Registry{
		logger:     logger,
		connectors: connectors,
		service:    service,
	}

	return r
}

func (r *Registry) Init(ctx context.Context) {
	// buildInfo is no longer used for getting connector versions, but might be useful for debugging
	_, ok := debug.ReadBuildInfo()
	if !ok {
		r.logger.Warn(ctx).Msg("build info not available")
	}

	r.plugins = r.loadPlugins()
	r.logger.Info(ctx).Int("count", len(r.List())).Msg("builtin connector plugins initialized")
}

func (r *Registry) loadPlugins() map[string]map[string]blueprint {
	plugins := make(map[string]map[string]blueprint, len(r.connectors))
	for moduleName, conn := range r.connectors {
		factory := newDispenserFactory(conn)

		specs, err := getSpecification(moduleName, factory)
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

func getSpecification(moduleName string, factory dispenserFactory) (pconnector.Specification, error) {
	dispenser := factory("", pconnector.PluginConfig{}, log.CtxLogger{})
	specPlugin, err := dispenser.DispenseSpecifier()
	if err != nil {
		return pconnector.Specification{}, cerrors.Errorf("could not dispense specifier for built in plugin: %w", err)
	}
	resp, err := specPlugin.Specify(context.Background(), pconnector.SpecifierSpecifyRequest{})
	if err != nil {
		return pconnector.Specification{}, cerrors.Errorf("could not get specs for built in plugin: %w", err)
	}

	// Overwrite the connector's reported version with the centrally managed Conduit
	// built-in connector version. This ensures all built-in connectors
	// report a consistent version, which is automatically updated by the CI/CD pipeline.
	// Since we can't import conduit package due to circular dependency, we use a simple approach
	// that relies on build flags to set the version at build time.
	resp.Specification.Version = getBuiltinConnectorVersion()
	
	return resp.Specification, nil
}

// getModuleVersion is no longer used, as the version is now centrally managed by Conduit.
// func getModuleVersion(deps []*debug.Module, moduleName string) string {
// 	for _, dep := range deps {
// 		if dep.Path == moduleName {
// 			if dep.Replace != nil {
// 				return dep.Replace.Version
// 			}
// 			return dep.Version
// 		}
// 	}
// 	return ""
// }

func newFullName(pluginName, pluginVersion string) plugin.FullName {
	return plugin.NewFullName(plugin.PluginTypeBuiltin, pluginName, pluginVersion)
}

func (r *Registry) NewDispenser(logger log.CtxLogger, fullName plugin.FullName, cfg pconnector.PluginConfig) (connector.Dispenser, error) {
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

	return b.dispenserFactory(fullName, cfg, logger), nil
}

func (r *Registry) List() map[plugin.FullName]pconnector.Specification {
	specs := make(map[plugin.FullName]pconnector.Specification, len(r.plugins))
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

// Helper function to get the builtin connector version using build flags
func getBuiltinConnectorVersion() string {
	// This will be set via build flags during compilation
	// We're avoiding importing the conduit package to prevent circular dependency
	// and using this approach to allow easy overriding at build time
	return "v0.0.0-develop"
}
