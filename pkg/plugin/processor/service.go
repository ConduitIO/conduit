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

//go:generate mockgen -source=service.go -destination=mock/registry.go -package=mock -mock_names=registry=Registry,standaloneRegistry=StandaloneRegistry . registry,standaloneRegistry

package processor

import (
	"context"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
)

type registry interface {
	NewProcessor(ctx context.Context, fullName plugin.FullName, id string) (sdk.Processor, error)
	List() map[plugin.FullName]sdk.Specification
}

type standaloneRegistry interface {
	registry
	Register(ctx context.Context, path string) (plugin.FullName, error)
}

type PluginService struct {
	logger log.CtxLogger

	builtinReg    registry
	standaloneReg standaloneRegistry
}

func NewPluginService(logger log.CtxLogger, br registry, sr standaloneRegistry) *PluginService {
	return &PluginService{
		logger:        logger.WithComponent("processor.PluginService"),
		builtinReg:    br,
		standaloneReg: sr,
	}
}

func (s *PluginService) Check(context.Context) error {
	return nil
}

func (s *PluginService) NewProcessor(ctx context.Context, pluginName string, id string) (sdk.Processor, error) {
	fullName := plugin.FullName(pluginName)
	switch fullName.PluginType() {
	// standalone processors take precedence
	// over built-in processors with the same name
	case plugin.PluginTypeStandalone:
		return s.standaloneReg.NewProcessor(ctx, fullName, id)
	case plugin.PluginTypeBuiltin:
		return s.builtinReg.NewProcessor(ctx, fullName, id)
	case plugin.PluginTypeAny:
		d, err := s.standaloneReg.NewProcessor(ctx, fullName, id)
		if err != nil {
			s.logger.Debug(ctx).Err(err).Msg("could not find standalone plugin dispenser, falling back to builtin plugin")
			d, err = s.builtinReg.NewProcessor(ctx, fullName, id)
		}
		return d, err
	default:
		return nil, cerrors.Errorf("invalid plugin name prefix %q", fullName.PluginType())
	}
}

// RegisterStandalonePlugin registers a standalone processor plugin from the
// specified path and returns the full name of the plugin or an error.
func (s *PluginService) RegisterStandalonePlugin(ctx context.Context, path string) (string, error) {
	fullName, err := s.standaloneReg.Register(ctx, path)
	if err != nil {
		return "", cerrors.Errorf("failed to register standalone processor: %w", err)
	}
	return string(fullName), nil
}

func (s *PluginService) List(context.Context) (map[string]sdk.Specification, error) {
	builtinSpecs := s.builtinReg.List()
	standaloneSpecs := s.standaloneReg.List()

	specs := make(map[string]sdk.Specification, len(builtinSpecs)+len(standaloneSpecs))
	for k, v := range builtinSpecs {
		specs[string(k)] = v
	}
	for k, v := range standaloneSpecs {
		specs[string(k)] = v
	}

	return specs, nil
}
