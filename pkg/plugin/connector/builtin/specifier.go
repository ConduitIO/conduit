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

package builtin

import (
	"context"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector"
)

type specifierPluginAdapter struct {
	impl pconnector.SpecifierPlugin
	// logger is used as the internal logger of specifierPluginAdapter.
	logger log.CtxLogger
}

var _ connector.SpecifierPlugin = (*specifierPluginAdapter)(nil)

func newSpecifierPluginAdapter(impl pconnector.SpecifierPlugin, logger log.CtxLogger) *specifierPluginAdapter {
	return &specifierPluginAdapter{
		impl:   impl,
		logger: logger.WithComponent("builtin.specifierPluginAdapter"),
	}
}

func (s *specifierPluginAdapter) Specify(ctx context.Context, in pconnector.SpecifierSpecifyRequest) (pconnector.SpecifierSpecifyResponse, error) {
	return runSandbox(s.impl.Specify, ctx, in, s.logger, "Specify")
}
