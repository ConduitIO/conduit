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

package builtin

import (
	"context"
	"runtime/debug"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/base64"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/cohere"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/field"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/json"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/ollama"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/openai"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/unwrap"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/webhook"
	"github.com/conduitio/conduit/pkg/plugin/processor/procutils"
	"github.com/conduitio/conduit/pkg/schemaregistry"
)

var DefaultBuiltinProcessors = map[string]ProcessorPluginConstructor{
	"avro.decode":         Constructor(avro.NewDecodeProcessor),
	"avro.encode":         Constructor(avro.NewEncodeProcessor),
	"base64.decode":       Constructor(base64.NewDecodeProcessor),
	"base64.encode":       Constructor(base64.NewEncodeProcessor),
	"clone":               Constructor(impl.NewCloneProcessor),
	"cohere.command":      Constructor(cohere.NewCommandProcessor),
	"cohere.embed":        Constructor(cohere.NewEmbedProcessor),
	"cohere.rerank":       Constructor(cohere.NewRerankProcessor),
	"custom.javascript":   Constructor(custom.NewJavascriptProcessor),
	"error":               Constructor(impl.NewErrorProcessor),
	"filter":              Constructor(impl.NewFilterProcessor),
	"field.convert":       Constructor(field.NewConvertProcessor),
	"field.exclude":       Constructor(field.NewExcludeProcessor),
	"field.rename":        Constructor(field.NewRenameProcessor),
	"field.set":           Constructor(field.NewSetProcessor),
	"json.decode":         Constructor(json.NewDecodeProcessor),
	"json.encode":         Constructor(json.NewEncodeProcessor),
	"ollama.request":      Constructor(ollama.NewOllamaProcessor),
	"openai.embed":        Constructor(openai.NewEmbeddingsProcessor),
	"openai.textgen":      Constructor(openai.NewTextgenProcessor),
	"split":               Constructor(impl.NewSplitProcessor),
	"unwrap.debezium":     Constructor(unwrap.NewDebeziumProcessor),
	"unwrap.kafkaconnect": Constructor(unwrap.NewKafkaConnectProcessor),
	"unwrap.opencdc":      Constructor(unwrap.NewOpenCDCProcessor),
	"webhook.http":        Constructor(webhook.NewHTTPProcessor),
}

func Constructor[T sdk.Processor](p func(log.CtxLogger) T) ProcessorPluginConstructor {
	return func(logger log.CtxLogger) sdk.Processor { return p(logger) }
}

type schemaRegistryProcessor interface {
	SetSchemaRegistry(schemaregistry.Registry)
}

type Registry struct {
	logger log.CtxLogger

	// plugins stores plugin blueprints in a 2D map, first key is the plugin
	// name, the second key is the plugin version
	plugins        map[string]map[string]blueprint
	schemaRegistry schemaregistry.Registry
}

type blueprint struct {
	fullName      plugin.FullName
	specification sdk.Specification
	constructor   ProcessorPluginConstructor
}

type ProcessorPluginConstructor func(log.CtxLogger) sdk.Processor

func NewRegistry(
	logger log.CtxLogger,
	constructors map[string]ProcessorPluginConstructor,
	schemaRegistry schemaregistry.Registry,
) *Registry {
	// set schema service and logger for builtin processors
	schema.SchemaService = procutils.NewSchemaService(logger, schemaRegistry)
	pprocutils.Logger = logger.WithComponent("processor").
		ZerologWithComponent().
		Hook(ctxutil.ProcessorIDLogCtxHook{})

	logger = logger.WithComponent("plugin.processor.builtin.Registry")
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		// we are using modules, build info should always be available, we are staying on the safe side
		logger.Warn(context.Background()).Msg("build info not available, built-in plugin versions may not be read correctly")
		buildInfo = &debug.BuildInfo{} // prevent nil pointer exceptions
	}

	r := &Registry{
		plugins:        loadPlugins(buildInfo, constructors),
		logger:         logger,
		schemaRegistry: schemaRegistry,
	}
	logger.Info(context.Background()).Int("count", len(r.List())).Msg("builtin processor plugins initialized")
	return r
}

func loadPlugins(buildInfo *debug.BuildInfo, constructors map[string]ProcessorPluginConstructor) map[string]map[string]blueprint {
	plugins := make(map[string]map[string]blueprint, len(constructors))
	for moduleName, constructor := range constructors {
		specs, err := getSpecification(moduleName, constructor, buildInfo)
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
			fullName:      fullName,
			constructor:   constructor,
			specification: specs,
		}
		versionMap[specs.Version] = bp

		latestBp, ok := versionMap[plugin.PluginVersionLatest]
		if !ok || fullName.PluginVersionGreaterThan(latestBp.fullName) {
			versionMap[plugin.PluginVersionLatest] = bp
		}
	}
	return plugins
}

func getSpecification(moduleName string, constructor ProcessorPluginConstructor, buildInfo *debug.BuildInfo) (sdk.Specification, error) {
	procPlugin := constructor(log.CtxLogger{})
	specs, err := procPlugin.Specification()
	if err != nil {
		return sdk.Specification{}, err
	}

	if version := getModuleVersion(buildInfo.Deps, moduleName); version != "" {
		// overwrite version with the import version
		specs.Version = version
	}

	return specs, nil
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

func (r *Registry) NewProcessor(_ context.Context, fullName plugin.FullName, id string) (sdk.Processor, error) {
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

	p := b.constructor(r.logger)
	if sr, setSR := p.(schemaRegistryProcessor); setSR {
		sr.SetSchemaRegistry(r.schemaRegistry)
	}

	// Apply default middleware since built-in processors are created
	// without the middleware applied.
	// This is done by Conduit only for the built-in processors.
	// In the standalone processors, the Run() method adds the middleware.
	p = sdk.ProcessorWithMiddleware(p, sdk.DefaultProcessorMiddleware(p.MiddlewareOptions()...)...)
	// attach processor ID for logs
	p = newProcessorWithID(p, id)

	return p, nil
}

func (r *Registry) List() map[plugin.FullName]sdk.Specification {
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
