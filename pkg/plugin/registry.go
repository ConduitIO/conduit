// Copyright © 2023 Meroxa, Inc.
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
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

type Spec interface {
	GetName() string
	GetVersion() string
}

type GetSpecFn func(ctx context.Context, logger zerolog.Logger, path string) (Spec, error)

// Registry is a generic directory registry of plugins,
// organized by plugin type, name and version.
// Every file in the specified directory is considered a plugin
// (directories are skipped).
// A registry is instantiated with a function, which can
// get specifications from a plugin, given its path on the file system.
type Registry struct {
	logger   log.CtxLogger
	getSpecs GetSpecFn

	pluginDir string
	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
	// m guards plugins from being concurrently accessed
	m sync.RWMutex
}

type blueprint struct {
	fullName      FullName
	specification Spec
	path          string
	// TODO store hash of plugin binary and compare before running the binary to
	// ensure someone can't switch the plugin after we registered it
}

func NewRegistry(
	logger log.CtxLogger,
	pluginDir string,
	getSpecs GetSpecFn,
) *Registry {
	r := &Registry{
		logger:   logger.WithComponent("standalone.Registry"),
		getSpecs: getSpecs,
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

func newFullName(pluginName, pluginVersion string) FullName {
	return NewFullName(PluginTypeStandalone, pluginName, pluginVersion)
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
		specs, err := r.getSpecs(ctx, zerolog.Nop(), pluginPath)
		if err != nil {
			err = cerrors.Errorf("failed to get specs: %w", err)
			warn(ctx, err, pluginPath)
			continue
		}

		versionMap := plugins[specs.GetName()]
		if versionMap == nil {
			versionMap = make(map[string]blueprint)
			plugins[specs.GetName()] = versionMap
		}

		fullName := newFullName(specs.GetName(), specs.GetVersion())
		if conflict, ok := versionMap[specs.GetVersion()]; ok {
			err = cerrors.Errorf("conflict detected, plugin %v already registered, please remove either %v or %v, these plugins won't be usable until that happens", fullName, conflict.path, pluginPath)
			warn(ctx, err, pluginPath)
			// delete plugin from map at the end so that further duplicates can
			// still be found
			defer func() {
				delete(versionMap, specs.GetVersion())
				if len(versionMap) == 0 {
					delete(plugins, specs.GetName())
				}
			}()
			continue
		}

		bp := blueprint{
			fullName:      fullName,
			specification: specs,
			path:          pluginPath,
		}
		versionMap[specs.GetVersion()] = bp

		latestFullName := versionMap[PluginVersionLatest].fullName
		if fullName.PluginVersionGreaterThan(latestFullName) {
			versionMap[PluginVersionLatest] = bp
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

func (r *Registry) List() map[FullName]Spec {
	r.m.RLock()
	defer r.m.RUnlock()

	specs := make(map[FullName]Spec, len(r.plugins))
	for _, versions := range r.plugins {
		for version, bp := range versions {
			if version == PluginVersionLatest {
				continue // skip latest versions
			}
			specs[bp.fullName] = bp.specification
		}
	}
	return specs
}
