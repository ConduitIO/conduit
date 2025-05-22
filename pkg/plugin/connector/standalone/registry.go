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
	"strconv"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/rs/zerolog"
)

type Registry struct {
	logger    log.CtxLogger
	pluginDir string

	connUtilsAddr        string
	maxReceiveRecordSize int

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
	// m guards plugins from being concurrently accessed
	m sync.RWMutex
}

type blueprint struct {
	FullName      plugin.FullName
	Specification pconnector.Specification
	Path          string
	// TODO store hash of plugin binary and compare before running the binary to
	// ensure someone can't switch the plugin after we registered it
}

func NewRegistry(logger log.CtxLogger, pluginDir string) *Registry {
	r := &Registry{
		logger: logger.WithComponentFromType(Registry{}),
	}

	if pluginDir != "" {
		// extract absolute path to make it clearer in the logs what directory is used
		absPluginDir, err := filepath.Abs(pluginDir)
		if err != nil {
			r.logger.Warn(context.Background()).Err(err).Msg("could not extract absolute connector plugins path")
		} else {
			r.pluginDir = absPluginDir // store plugin dir for hot reloads
		}
	}

	return r
}

func (r *Registry) Init(ctx context.Context, connUtilsAddr string, maxReceiveRecordSize int) {
	r.connUtilsAddr = connUtilsAddr
	r.maxReceiveRecordSize = maxReceiveRecordSize

	plugins := r.loadPlugins(ctx)
	r.m.Lock()
	r.plugins = plugins
	r.m.Unlock()

	r.logger.Info(ctx).
		Str(log.PluginPathField, r.pluginDir).
		Int("count", len(r.List())).
		Msg("standalone connector plugins initialized")
}

func (r *Registry) loadPlugins(ctx context.Context) map[string]map[string]blueprint {
	r.logger.Debug(ctx).Msgf("loading connector plugins from directory %v", r.pluginDir)
	plugins := make(map[string]map[string]blueprint)

	dirEntries, err := os.ReadDir(r.pluginDir)
	if err != nil {
		r.logger.Warn(ctx).Err(err).Msg("could not read connector plugin directory")
		return plugins // return empty map
	}
	warn := func(ctx context.Context, err error, pluginPath string) {
		r.logger.Warn(ctx).
			Err(err).
			Str(log.PluginPathField, pluginPath).
			Msgf("could not load standalone connector plugin")
	}

	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			// skip directories
			continue
		}

		pluginPath := path.Join(r.pluginDir, dirEntry.Name())

		specs, err := r.loadSpecifications(pluginPath)
		if err != nil {
			warn(ctx, err, pluginPath)
			continue
		}

		versionMap := plugins[specs.Name]
		if versionMap == nil {
			versionMap = make(map[string]blueprint)
			plugins[specs.Name] = versionMap
		}

		fullName := plugin.NewFullName(plugin.PluginTypeStandalone, specs.Name, specs.Version)
		if conflict, ok := versionMap[specs.Version]; ok {
			err = cerrors.Errorf("failed to load plugin %v from %v: %w (conflicts with %v)", fullName, pluginPath, plugin.ErrPluginAlreadyRegistered, conflict.Path)
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
			FullName:      fullName,
			Specification: specs,
			Path:          pluginPath,
		}
		versionMap[specs.Version] = bp

		latestFullName := versionMap[plugin.PluginVersionLatest].FullName
		if fullName.PluginVersionGreaterThan(latestFullName) {
			versionMap[plugin.PluginVersionLatest] = bp
			r.logger.Debug(ctx).
				Str(log.PluginPathField, pluginPath).
				Str(log.PluginNameField, string(bp.FullName)).
				Msg("set connector plugin as latest")
		}

		r.logger.Debug(ctx).
			Str(log.PluginPathField, pluginPath).
			Str(log.PluginNameField, string(bp.FullName)).
			Msg("loaded standalone connector plugin")
	}

	return plugins
}

func (r *Registry) loadSpecifications(pluginPath string) (pconnector.Specification, error) {
	// create dispenser without a logger to not spam logs on refresh
	dispenser, err := NewDispenser(
		zerolog.Nop(),
		pluginPath,
		client.WithEnvVar(pconnutils.EnvConduitConnectorUtilitiesGRPCTarget, r.connUtilsAddr),
		client.WithEnvVar(pconnutils.EnvConduitConnectorToken, "irrelevant-token"),
		client.WithEnvVar(pconnector.EnvConduitConnectorID, "load-specifications"),
	)
	if err != nil {
		return pconnector.Specification{}, cerrors.Errorf("failed to create connector dispenser: %w", err)
	}

	specPlugin, err := dispenser.DispenseSpecifier()
	if err != nil {
		return pconnector.Specification{}, cerrors.Errorf("failed to dispense connector specifier (tip: check if the file is a valid connector plugin binary and if you have permissions for running it): %w", err)
	}

	resp, err := specPlugin.Specify(context.Background(), pconnector.SpecifierSpecifyRequest{})
	if err != nil {
		return pconnector.Specification{}, cerrors.Errorf("failed to get connector specs: %w", err)
	}

	return resp.Specification, nil
}

func (r *Registry) NewDispenser(logger log.CtxLogger, fullName plugin.FullName, cfg pconnector.PluginConfig) (connector.Dispenser, error) {
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
		return nil, cerrors.Errorf("could not find standalone connector plugin, only found versions %v: %w", availableVersions, plugin.ErrPluginNotFound)
	}

	logger = logger.WithComponent("plugin.standalone")
	return NewDispenser(
		logger.ZerologWithComponent(),
		bp.Path,
		client.WithEnvVar(pconnutils.EnvConduitConnectorUtilitiesGRPCTarget, r.connUtilsAddr),
		client.WithEnvVar(pconnutils.EnvConduitConnectorToken, cfg.Token),
		client.WithEnvVar(pconnector.EnvConduitConnectorID, cfg.ConnectorID),
		client.WithEnvVar(pconnector.EnvConduitConnectorLogLevel, cfg.LogLevel),
		client.WithEnvVar(pconnector.EnvConduitConnectorMaxReceiveRecordSize, strconv.Itoa(r.maxReceiveRecordSize)),
	)
}

func (r *Registry) List() map[plugin.FullName]pconnector.Specification {
	r.m.RLock()
	defer r.m.RUnlock()

	specs := make(map[plugin.FullName]pconnector.Specification, len(r.plugins))
	for _, versions := range r.plugins {
		for version, bp := range versions {
			if version == plugin.PluginVersionLatest {
				continue // skip latest versions
			}
			specs[bp.FullName] = bp.Specification
		}
	}
	return specs
}
