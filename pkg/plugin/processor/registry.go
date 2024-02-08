// Copyright © 2024 Meroxa, Inc.
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

//go:generate mockgen -source=registry.go -destination=mock/processor_creator.go -package=mock -mock_names=processorCreator=ProcessorCreator . processorCreator

package processor

import (
	"context"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

type processorCreator interface {
	NewProcessor(ctx context.Context, fullName plugin.FullName, id string) (sdk.Processor, error)
}

type Registry struct {
	logger log.CtxLogger

	builtinReg    processorCreator
	standaloneReg processorCreator
}

func NewRegistry(logger log.CtxLogger, br processorCreator, sr processorCreator) *Registry {
	return &Registry{
		logger:        logger,
		builtinReg:    br,
		standaloneReg: sr,
	}
}

func (r *Registry) Get(ctx context.Context, pluginName string, id string) (sdk.Processor, error) {
	fullName := plugin.FullName(pluginName)
	switch fullName.PluginType() {
	// standalone processors take precedence
	// over built-in processors with the same name
	case plugin.PluginTypeStandalone:
		return r.standaloneReg.NewProcessor(ctx, fullName, id)
	case plugin.PluginTypeBuiltin:
		return r.builtinReg.NewProcessor(ctx, fullName, id)
	case plugin.PluginTypeAny:
		d, err := r.standaloneReg.NewProcessor(ctx, fullName, id)
		if err != nil {
			r.logger.Debug(ctx).Err(err).Msg("could not find standalone plugin dispenser, falling back to builtin plugin")
			d, err = r.builtinReg.NewProcessor(ctx, fullName, id)
		}
		return d, err
	default:
		return nil, cerrors.Errorf("invalid plugin name prefix %q", fullName.PluginType())
	}
}
