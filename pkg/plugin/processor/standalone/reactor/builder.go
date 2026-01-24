// Copyright © 2025 Meroxa, Inc.
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

package reactor

import (
	"context"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
)

type Builder struct {
	logger log.CtxLogger

	runtime wazero.Runtime
	// hostModule is the conduit host module that exposes Conduit host functions
	// to the WASM module. The host module is compiled once and instantiated
	// multiple times, once for each WASM module.
	hostModule    *wazergo.CompiledModule[*HostModuleInstance]
	schemaService pprocutils.SchemaService
}

func NewBuilder(
	ctx context.Context,
	logger log.CtxLogger,
	runtime wazero.Runtime,
	schemaService pprocutils.SchemaService,
) (*Builder, error) {
	logger = logger.WithComponentFromType(Builder{})

	// init host module
	compiledHostModule, err := wazergo.Compile(ctx, runtime, HostModule)
	if err != nil {
		return nil, cerrors.Errorf("failed to compile host module: %w", err)
	}

	return &Builder{
		logger:        logger,
		runtime:       runtime,
		hostModule:    compiledHostModule,
		schemaService: schemaService,
	}, nil
}

func (b *Builder) Build(
	ctx context.Context,
	processorModule wazero.CompiledModule,
	id string,
) (*WasmProcessor, error) {
	return NewWasmProcessor(ctx, b.runtime, processorModule, b.hostModule, b.schemaService, id, b.logger)
}
