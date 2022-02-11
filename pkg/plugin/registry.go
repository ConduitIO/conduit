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

package plugin

import (
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

const builtinPluginPrefix = "builtin:"

// registry is an object that can create new plugin dispensers. We need to use
// an interface to prevent a cyclic dependency between the plugin package and
// builtin and standalone packages.
// There are two registries that implement this interface:
// * The builtin registry create a dispenser which dispenses a plugin adapter
//   that communicates with the plugin directly as if it was a library. These
//   plugins are baked into the Conduit binary and included at compile time.
// * The standalone registry creates a dispenser which starts the plugin in a
//   separate process and communicates with it via gRPC. These plugins are
//   compiled independently of Conduit and can be included at runtime.
type registry interface {
	New(logger log.CtxLogger, name string) (Dispenser, error)
}

type Registry struct {
	builtin    registry
	standalone registry
}

func NewRegistry(builtin registry, standalone registry) *Registry {
	return &Registry{
		builtin:    builtin,
		standalone: standalone,
	}
}

func (r *Registry) New(logger log.CtxLogger, name string) (Dispenser, error) {
	logger = logger.WithComponent("plugin")

	if strings.HasPrefix(name, builtinPluginPrefix) {
		return r.builtin.New(logger, strings.TrimPrefix(name, builtinPluginPrefix))
	}

	return r.standalone.New(logger, name)
}
