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

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/rs/zerolog"
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
			func(ctx context.Context, logger zerolog.Logger, path string) (plugin.Spec, error) {
				p, err := NewWASMProcessor(ctx, logger, path)
				if err != nil {
					return nil, fmt.Errorf("failed creating a new WASM processor: %w", err)
				}
				defer func() {
					err := p.Teardown(ctx)
					if err != nil {
						logger.Warn().Err(err).Msg("processor teardown failed")
					}
				}()

				s, err := p.Specification()
				if err != nil {
					return nil, err
				}
				return &spec{Specification: s}, nil
			},
		),
	}
}
