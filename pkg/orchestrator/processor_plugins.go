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

package orchestrator

import (
	"context"

	processorSdk "github.com/conduitio/conduit-processor-sdk"
)

type ProcessorPluginOrchestrator base

func (ps *ProcessorPluginOrchestrator) List(ctx context.Context) (map[string]processorSdk.Specification, error) {
	return ps.processorPlugins.List(ctx)
}

func (ps *ProcessorPluginOrchestrator) RegisterStandalonePlugin(ctx context.Context, path string) (string, error) {
	return ps.processorPlugins.RegisterStandalonePlugin(ctx, path)
}
