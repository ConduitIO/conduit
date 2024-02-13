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

package builtinv1

import (
	"context"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin/v1/internal/toplugin"
)

type specifierPluginAdapter struct {
	impl cpluginv1.SpecifierPlugin
	// logger is used as the internal logger of specifierPluginAdapter.
	logger log.CtxLogger
}

var _ plugin.SpecifierPlugin = (*specifierPluginAdapter)(nil)

func newSpecifierPluginAdapter(impl cpluginv1.SpecifierPlugin, logger log.CtxLogger) *specifierPluginAdapter {
	return &specifierPluginAdapter{
		impl:   impl,
		logger: logger.WithComponent("builtinv1.specifierPluginAdapter"),
	}
}

func (s *specifierPluginAdapter) Specify() (plugin.Specification, error) {
	req := toplugin.SpecifierSpecifyRequest()
	resp, err := runSandbox(s.impl.Specify, context.Background(), req, s.logger)
	if err != nil {
		return plugin.Specification{}, err
	}
	out, err := fromplugin.SpecifierSpecifyResponse(resp)
	if err != nil {
		return plugin.Specification{}, err
	}
	return out, nil
}
