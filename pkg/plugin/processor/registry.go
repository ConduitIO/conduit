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

package processor

import (
	"context"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone"
)

type Registry struct {
	logger log.CtxLogger

	BuiltinReg    *builtin.Registry
	StandaloneReg *standalone.Registry
}

func (r *Registry) Get(ctx context.Context, pluginName string, id string) (sdk.Processor, error) {
	// todo use legacy processors here as well
	// todo check if registries are nil
	fullName := plugin.FullName(pluginName)
	switch fullName.PluginType() {
	case plugin.PluginTypeStandalone:
		return r.StandaloneReg.NewProcessor(ctx, fullName, id)
	case plugin.PluginTypeBuiltin:
		return r.BuiltinReg.NewProcessorPlugin(r.logger, fullName)
	case plugin.PluginTypeAny:
		d, err := r.StandaloneReg.NewProcessor(ctx, fullName, id)
		if err != nil {
			r.logger.Debug(context.Background()).Err(err).Msg("could not find standalone plugin dispenser, falling back to builtin plugin")
			d, err = r.BuiltinReg.NewProcessorPlugin(r.logger, fullName)
		}
		return d, err
	default:
		return nil, cerrors.Errorf("invalid plugin name prefix %q", fullName.PluginType())
	}
}
