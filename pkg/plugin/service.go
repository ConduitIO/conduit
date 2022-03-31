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
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const BuiltinPluginPrefix = "builtin:"

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
	List() (map[string]Specification, error)
}

type Service struct {
	builtin    registry
	standalone registry
}

func NewService(builtin registry, standalone registry) *Service {
	return &Service{
		builtin:    builtin,
		standalone: standalone,
	}
}

func (r *Service) NewDispenser(logger log.CtxLogger, name string) (Dispenser, error) {
	logger = logger.WithComponent("plugin")

	if strings.HasPrefix(name, BuiltinPluginPrefix) {
		return r.builtin.New(logger, strings.TrimPrefix(name, BuiltinPluginPrefix))
	}

	return r.standalone.New(logger, name)
}

func (r *Service) List(ctx context.Context) (map[string]Specification, error) {
	// todo: attach standalone list
	specs, err := r.builtin.List()
	if err != nil {
		return nil, cerrors.Errorf("failed to list the builtin plugins: %w", err)
	}

	return specs, nil
}
