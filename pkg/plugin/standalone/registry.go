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

package standalone

import (
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	standalonev1 "github.com/conduitio/conduit/pkg/plugin/standalone/v1"
)

type Registry struct {
	logger log.CtxLogger
}

func NewRegistry(logger log.CtxLogger) *Registry {
	return &Registry{
		logger: logger,
	}
}

func (r *Registry) New(logger log.CtxLogger, path string) (plugin.Dispenser, error) {
	return standalonev1.NewDispenser(logger.ZerologWithComponent(), path)
}
