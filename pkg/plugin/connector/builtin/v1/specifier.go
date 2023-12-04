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
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/fromplugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin/v1/internal/toplugin"
)

// TODO make sure a panic in a plugin doesn't crash Conduit
type specifierPluginAdapter struct {
	impl cpluginv1.SpecifierPlugin
}

var _ connector.SpecifierPlugin = (*specifierPluginAdapter)(nil)

func newSpecifierPluginAdapter(impl cpluginv1.SpecifierPlugin) *specifierPluginAdapter {
	return &specifierPluginAdapter{
		impl: impl,
	}
}

func (s *specifierPluginAdapter) Specify() (connector.Specification, error) {
	req := toplugin.SpecifierSpecifyRequest()
	resp, err := runSandbox(s.impl.Specify, context.Background(), req)
	if err != nil {
		return connector.Specification{}, err
	}
	out, err := fromplugin.SpecifierSpecifyResponse(resp)
	if err != nil {
		return connector.Specification{}, err
	}
	return out, nil
}
