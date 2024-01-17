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

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

type Registry struct {
	*plugin.Registry
}

type spec struct {
	sdk.Specification
}

func (s *spec) GetName() string {
	return s.Name
}

func (s *spec) GetVersion() string {
	return s.Version
}

func NewRegistry(logger log.CtxLogger, pluginDir string) *Registry {
	return &Registry{
		Registry: plugin.NewRegistry(
			logger,
			pluginDir,
			func(ctx context.Context, logger log.CtxLogger, path string) (plugin.Spec, error) {
				// p, err := NewWASMProcessor(ctx, logger, path)
				// if err != nil {
				// 	return nil, fmt.Errorf("failed creating a new WASM processor: %w", err)
				// }
				// defer func() {
				// 	err := p.Teardown(ctx)
				// 	if err != nil {
				// 		logger.Warn(ctx).Err(err).Msg("processor teardown failed")
				// 	}
				// }()
				//
				// s, err := p.Specification()
				// if err != nil {
				// 	return nil, err
				// }
				// return &spec{Specification: s}, nil
				panic("not implemented")
			},
		),
	}
}

// r := wazero.NewRuntimeWithConfig(
// 	ctx,
// 	wazero.NewRuntimeConfig().WithCloseOnContextDone(true),
// )
// defer func() {
// 	if err != nil {
// 		// If there was an error, close the runtime
// 		closeErr := r.Close(ctx)
// 		if closeErr != nil {
// 			logger.Err(ctx, closeErr).Msg("failed to close wazero runtime")
// 		}
// 	}
// }()
//
// _, err = wasi_snapshot_preview1.Instantiate(ctx, r)
// if err != nil {
// 	return nil, cerrors.Errorf("failed instantiating wasi_snapshot_preview1: %w", err)
// }

// 	p.logger.
// 		Debug(ctx).
// 		Str("path", path).
// 		Msg("running WASM processor")
//
// 	wasmBytes, err := os.ReadFile(path)
// 	if err != nil {
// 		return fmt.Errorf("failed reading WASM file %s: %w", path, err)
// 	}
//
// 	// Compiling a module helps check for some errors early on
// 	p.logger.
// 		Debug(ctx).
// 		Str("path", path).
// 		Msg("compiling module")
// 	mod, err := p.runtime.CompileModule(ctx, wasmBytes)
// 	if err != nil {
// 		return fmt.Errorf("failed compiling WASM module: %w", err)
// 	}
