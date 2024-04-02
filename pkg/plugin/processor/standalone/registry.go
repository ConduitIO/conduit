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

package standalone

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/stealthrocket/wazergo"

	"github.com/tetratelabs/wazero"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

// Registry is a directory registry of processor plugins, organized by plugin
// type, name and version.
// Every file in the specified directory is considered a plugin
// (directories are skipped).
type Registry struct {
	logger    log.CtxLogger
	pluginDir string
	runtime   wazero.Runtime

	// hostModule is the conduit host module that exposes Conduit host functions
	// to the WASM module. The host module is compiled once and instantiated
	// multiple times, once for each WASM module.
	hostModule *wazergo.CompiledModule[*hostModuleInstance]

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins map[string]map[string]blueprint
	// m guards plugins from being concurrently accessed
	m sync.RWMutex
}

type blueprint struct {
	fullName      plugin.FullName
	specification sdk.Specification
	path          string
	module        wazero.CompiledModule
	// TODO store hash of plugin binary and compare before running the binary to
	// ensure someone can't switch the plugin after we registered it
}

func NewRegistry(logger log.CtxLogger, pluginDir string) (*Registry, error) {
	// context is only used for logging, it's not used for long running operations
	ctx := context.Background()

	logger = logger.WithComponentFromType(Registry{})

	if pluginDir != "" {
		// extract absolute path to make it clearer in the logs what directory is used
		absPluginDir, err := filepath.Abs(pluginDir)
		if err != nil {
			logger.Warn(ctx).Err(err).Msg("could not extract absolute processor plugins path")
		} else {
			pluginDir = absPluginDir
		}
	}

	// we are using the wasm compiler, context is not used
	runtime := wazero.NewRuntime(ctx)
	// TODO close runtime on shutdown

	_, err := wasi_snapshot_preview1.Instantiate(ctx, runtime)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, cerrors.Errorf("failed to instantiate WASI: %w", err)
	}

	// init host module
	compiledHostModule, err := wazergo.Compile(ctx, runtime, hostModule)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, cerrors.Errorf("failed to compile host module: %w", err)
	}

	r := &Registry{
		logger:     logger,
		runtime:    runtime,
		hostModule: compiledHostModule,
		pluginDir:  pluginDir,
	}

	r.reloadPlugins()
	r.logger.Info(context.Background()).
		Str(log.PluginPathField, r.pluginDir).
		Int("count", len(r.List())).
		Msg("standalone processor plugins initialized")

	return r, nil
}

func (r *Registry) NewProcessor(ctx context.Context, fullName plugin.FullName, id string) (sdk.Processor, error) {
	r.m.RLock()
	defer r.m.RUnlock()

	versions, ok := r.plugins[fullName.PluginName()]
	if !ok {
		return nil, plugin.ErrPluginNotFound
	}
	bp, ok := versions[fullName.PluginVersion()]
	if !ok {
		availableVersions := make([]string, 0, len(versions))
		for k := range versions {
			availableVersions = append(availableVersions, k)
		}
		return nil, cerrors.Errorf("could not find standalone processor plugin, only found versions %v: %w", availableVersions, plugin.ErrPluginNotFound)
	}

	p, err := newWASMProcessor(ctx, r.runtime, bp.module, r.hostModule, id, r.logger)
	if err != nil {
		return nil, cerrors.Errorf("failed to create a new WASM processor: %w", err)
	}

	return p, nil
}

func (r *Registry) reloadPlugins() {
	if r.pluginDir == "" {
		return // no plugin dir, no plugins to load
	}

	plugins := r.loadPlugins(context.Background(), r.pluginDir)
	r.m.Lock()
	r.plugins = plugins
	r.m.Unlock()
}

func (r *Registry) loadPlugins(ctx context.Context, pluginDir string) map[string]map[string]blueprint {
	r.logger.Info(ctx).Msgf("loading processor plugins from directory %v ...", pluginDir)
	plugins := make(map[string]map[string]blueprint)

	dirEntries, err := os.ReadDir(pluginDir)
	if err != nil {
		r.logger.Warn(ctx).Err(err).Msg("could not read processor plugin directory")
		return plugins // return empty map
	}
	warn := func(ctx context.Context, err error, pluginPath string) {
		r.logger.Warn(ctx).
			Err(err).
			Str(log.PluginPathField, pluginPath).
			Msgf("could not load standalone processor plugin")
	}

	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			// skip directories
			continue
		}

		pluginPath := path.Join(pluginDir, dirEntry.Name())

		// create dispenser without a logger to not spam logs on refresh
		module, specs, err := r.loadModuleAndSpecifications(ctx, pluginPath)
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
			err = cerrors.Errorf("conflict detected, processor plugin %v already registered, please remove either %v or %v, these plugins won't be usable until that happens", fullName, conflict.path, pluginPath)
			warn(ctx, err, pluginPath)
			// close module as we won't use it
			_ = module.Close(ctx)
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
			module:        module,
		}
		versionMap[specs.Version] = bp

		latestFullName := versionMap[plugin.PluginVersionLatest].fullName
		if fullName.PluginVersionGreaterThan(latestFullName) {
			versionMap[plugin.PluginVersionLatest] = bp
			r.logger.Debug(ctx).
				Str(log.PluginPathField, pluginPath).
				Str(log.PluginNameField, string(bp.fullName)).
				Msg("set processor plugin as latest")
		}

		r.logger.Debug(ctx).
			Str(log.PluginPathField, pluginPath).
			Str(log.PluginNameField, string(bp.fullName)).
			Msg("loaded standalone processor plugin")
	}

	return plugins
}

func (r *Registry) loadModuleAndSpecifications(ctx context.Context, pluginPath string) (_ wazero.CompiledModule, _ sdk.Specification, err error) {
	wasmBytes, err := os.ReadFile(pluginPath)
	if err != nil {
		return nil, sdk.Specification{}, fmt.Errorf("failed to read WASM file %q: %w", pluginPath, err)
	}

	r.logger.Debug(ctx).
		Str("path", pluginPath).
		Msg("compiling WASM module")

	module, err := r.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, sdk.Specification{}, fmt.Errorf("failed to compile WASM module: %w", err)
	}
	defer func() {
		if err != nil {
			_ = module.Close(ctx)
		}
	}()

	p, err := newWASMProcessor(ctx, r.runtime, module, r.hostModule, "init-processor", log.Nop())
	if err != nil {
		return nil, sdk.Specification{}, fmt.Errorf("failed to create a new WASM processor: %w", err)
	}
	defer func() {
		err := p.Teardown(ctx)
		if err != nil {
			r.logger.Warn(ctx).Err(err).Msg("processor teardown failed")
		}
	}()

	specs, err := p.Specification()
	if err != nil {
		return nil, sdk.Specification{}, err
	}

	return module, specs, nil
}

func (r *Registry) List() map[plugin.FullName]sdk.Specification {
	r.m.RLock()
	defer r.m.RUnlock()

	specs := make(map[plugin.FullName]sdk.Specification, len(r.plugins))
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
