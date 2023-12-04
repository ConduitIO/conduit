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

package standalone

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	standalonev1 "github.com/conduitio/conduit/pkg/plugin/connector/standalone/v1"
	"github.com/rs/zerolog"
)

type Registry struct {
	logger    log.CtxLogger
	pluginDir string

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
	// m guards plugins from being concurrently accessed
	m sync.RWMutex
}

type blueprint struct {
	fullName      plugin.FullName
	specification connector.Specification
	path          string
	// TODO store hash of plugin binary and compare before running the binary to
	// ensure someone can't switch the plugin after we registered it
}

func NewRegistry(logger log.CtxLogger, pluginDir string) *Registry {
	r := &Registry{
		logger: logger.WithComponent("standalone.Registry"),
	}

	if pluginDir != "" {
		// extract absolute path to make it clearer in the logs what directory is used
		absPluginDir, err := filepath.Abs(pluginDir)
		if err != nil {
			r.logger.Warn(context.Background()).Err(err).Msg("could not extract absolute plugins path")
		} else {
			r.pluginDir = absPluginDir // store plugin dir for hot reloads
			r.reloadPlugins()
		}
	}

	r.logger.Info(context.Background()).
		Str(log.PluginPathField, r.pluginDir).
		Int("count", len(r.List())).
		Msg("standalone plugins initialized")

	return r
}

func newFullName(pluginName, pluginVersion string) plugin.FullName {
	return plugin.NewFullName(plugin.PluginTypeStandalone, pluginName, pluginVersion)
}

func (r *Registry) reloadPlugins() {
	plugins := r.loadPlugins(context.Background(), r.pluginDir)
	r.m.Lock()
	r.plugins = plugins
	r.m.Unlock()
}

func (r *Registry) loadPlugins(ctx context.Context, pluginDir string) map[string]map[string]blueprint {
	r.logger.Debug(ctx).Msgf("loading plugins from directory %v", pluginDir)
	plugins := make(map[string]map[string]blueprint)

	dirEntries, err := os.ReadDir(pluginDir)
	if err != nil {
		r.logger.Warn(ctx).Err(err).Msg("could not read plugin directory")
		return plugins // return empty map
	}
	warn := func(ctx context.Context, err error, pluginPath string) {
		r.logger.Warn(ctx).
			Err(err).
			Str(log.PluginPathField, pluginPath).
			Msgf("could not load standalone plugin")
	}

	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			// skip directories
			continue
		}

		pluginPath := path.Join(pluginDir, dirEntry.Name())

		// create dispenser without a logger to not spam logs on refresh
		dispenser, err := standalonev1.NewDispenser(zerolog.Nop(), pluginPath)
		if err != nil {
			err = cerrors.Errorf("failed to create dispenser: %w", err)
			warn(ctx, err, pluginPath)
			continue
		}

		specPlugin, err := dispenser.DispenseSpecifier()
		if err != nil {
			err = cerrors.Errorf("failed to dispense specifier (tip: check if the file is a valid plugin binary and if you have permissions for running it): %w", err)
			warn(ctx, err, pluginPath)
			continue
		}

		specs, err := specPlugin.Specify()
		if err != nil {
			err = cerrors.Errorf("failed to get specs: %w", err)
			warn(ctx, err, pluginPath)
			continue
		}

		versionMap := plugins[specs.Name]
		if versionMap == nil {
			versionMap = make(map[string]blueprint)
			plugins[specs.Name] = versionMap
		}

		fullName := newFullName(specs.Name, specs.Version)
		if conflict, ok := versionMap[specs.Version]; ok {
			err = cerrors.Errorf("conflict detected, plugin %v already registered, please remove either %v or %v, these plugins won't be usable until that happens", fullName, conflict.path, pluginPath)
			warn(ctx, err, pluginPath)
			// delete plugin from map at the end so that further duplicates can
			// still be found
			defer func() {
				delete(versionMap, specs.Version)
				if len(versionMap) == 0 {
					delete(plugins, specs.Name)
				}
			}()
			continue
		}

		bp := blueprint{
			fullName:      fullName,
			specification: specs,
			path:          pluginPath,
		}
		versionMap[specs.Version] = bp

		latestFullName := versionMap[plugin.PluginVersionLatest].fullName
		if fullName.PluginVersionGreaterThan(latestFullName) {
			versionMap[plugin.PluginVersionLatest] = bp
			r.logger.Debug(ctx).
				Str(log.PluginPathField, pluginPath).
				Str(log.PluginNameField, string(bp.fullName)).
				Msg("set plugin as latest")
		}

		r.logger.Debug(ctx).
			Str(log.PluginPathField, pluginPath).
			Str(log.PluginNameField, string(bp.fullName)).
			Msg("loaded standalone plugin")
	}

	return plugins
}

func (r *Registry) NewDispenser(logger log.CtxLogger, fullName plugin.FullName) (connector.Dispenser, error) {
	r.m.RLock()
	defer r.m.RUnlock()

	versionMap, ok := r.plugins[fullName.PluginName()]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	bp, ok := versionMap[fullName.PluginVersion()]
	if !ok {
		availableVersions := make([]string, 0, len(versionMap))
		for k := range versionMap {
			availableVersions = append(availableVersions, k)
		}
		return nil, cerrors.Errorf("could not find standalone plugin, only found versions %v: %w", availableVersions, plugin.ErrPluginNotFound)
	}

	return standalonev1.NewDispenser(logger.ZerologWithComponent(), bp.path)
}

func (r *Registry) List() map[plugin.FullName]connector.Specification {
	r.m.RLock()
	defer r.m.RUnlock()

	specs := make(map[plugin.FullName]connector.Specification, len(r.plugins))
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
