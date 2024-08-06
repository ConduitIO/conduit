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

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// newRuntime is a function that creates a new Wazero runtime. This is used to
// allow tests to replace the runtime with an interpreter runtime, as it's much
// faster than the compiler runtime.
var newRuntime = wazero.NewRuntime

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

	schemaService pprocutils.SchemaService

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

func NewRegistry(logger log.CtxLogger, pluginDir string, schemaService pprocutils.SchemaService) (*Registry, error) {
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
	runtime := newRuntime(ctx)
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
		logger:        logger,
		runtime:       runtime,
		hostModule:    compiledHostModule,
		pluginDir:     pluginDir,
		schemaService: schemaService,
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

	p, err := newWASMProcessor(ctx, r.runtime, bp.module, r.hostModule, r.schemaService, id, r.logger)
	if err != nil {
		return nil, cerrors.Errorf("failed to create a new WASM processor: %w", err)
	}

	return p, nil
}

// Register registers a standalone processor plugin from the specified path.
// If a plugin with the same name and version is already registered, it will
// return plugin.ErrPluginAlreadyRegistered.
func (r *Registry) Register(ctx context.Context, path string) (plugin.FullName, error) {
	bp, err := r.loadBlueprint(ctx, path)
	if err != nil {
		return "", err
	}

	r.m.Lock()
	defer r.m.Unlock()
	_, err = r.addBlueprint(r.plugins, bp)
	if err != nil {
		// close module as we won't use it
		_ = bp.module.Close(ctx)
		return bp.fullName, err
	}

	return bp.fullName, nil
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

		bp, err := r.loadBlueprint(ctx, pluginPath)
		if err != nil {
			warn(ctx, err, pluginPath)
			continue
		}

		isLatestVersion, err := r.addBlueprint(plugins, bp)
		if err != nil {
			warn(ctx, err, bp.path)
			// close module as we won't use it
			_ = bp.module.Close(ctx)
			// delete plugin from map at the end so that further duplicates can
			// still be found
			defer func() {
				delete(plugins[bp.specification.Name], bp.specification.Version)
				if len(plugins[bp.specification.Name]) == 0 {
					delete(plugins, bp.specification.Name)
				}
			}()
			continue
		}

		if isLatestVersion {
			r.logger.Debug(ctx).
				Str(log.PluginPathField, bp.path).
				Str(log.PluginNameField, string(bp.fullName)).
				Msg("set processor plugin as latest")
		}

		r.logger.Debug(ctx).
			Str(log.PluginPathField, bp.path).
			Str(log.PluginNameField, string(bp.fullName)).
			Msg("loaded standalone processor plugin")
	}

	return plugins
}

func (r *Registry) loadBlueprint(ctx context.Context, path string) (blueprint, error) {
	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return blueprint{}, fmt.Errorf("failed to read WASM file %q: %w", path, err)
	}

	r.logger.Debug(ctx).
		Str("path", path).
		Msg("compiling WASM module")

	module, err := r.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return blueprint{}, fmt.Errorf("failed to compile WASM module: %w", err)
	}
	defer func() {
		if err != nil {
			_ = module.Close(ctx)
		}
	}()

	p, err := newWASMProcessor(ctx, r.runtime, module, r.hostModule, r.schemaService, "init-processor", log.Nop())
	if err != nil {
		return blueprint{}, fmt.Errorf("failed to create a new WASM processor: %w", err)
	}
	defer func() {
		err := p.Teardown(ctx)
		if err != nil {
			r.logger.Warn(ctx).Err(err).Msg("processor teardown failed")
		}
	}()

	specs, err := p.Specification()
	if err != nil {
		return blueprint{}, err
	}

	return blueprint{
		fullName:      plugin.NewFullName(plugin.PluginTypeStandalone, specs.Name, specs.Version),
		specification: specs,
		path:          path,
		module:        module,
	}, nil
}

func (r *Registry) addBlueprint(plugins map[string]map[string]blueprint, bp blueprint) (isLatestVersion bool, err error) {
	versionMap := plugins[bp.specification.Name]
	if versionMap == nil {
		versionMap = make(map[string]blueprint)
		plugins[bp.specification.Name] = versionMap
	} else if conflict, ok := versionMap[bp.specification.Version]; ok {
		return false, cerrors.Errorf("failed to register plugin %v from %v: %w (conflicts with %v)", bp.fullName, bp.path, plugin.ErrPluginAlreadyRegistered, conflict.path)
	}

	versionMap[bp.specification.Version] = bp

	latestFullName := versionMap[plugin.PluginVersionLatest].fullName
	if bp.fullName.PluginVersionGreaterThan(latestFullName) {
		versionMap[plugin.PluginVersionLatest] = bp
		isLatestVersion = true
	}

	return isLatestVersion, nil
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
