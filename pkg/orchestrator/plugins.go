package orchestrator

import (
	"context"
	"github.com/conduitio/conduit/pkg/plugin"
)

type PluginOrchestrator base

func (ps *PluginOrchestrator) List(ctx context.Context) (map[string]plugin.Specification, error) {
	return ps.plugins.List(ctx)
}
