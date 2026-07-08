// Copyright © 2026 Meroxa, Inc.
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

package validate

import "github.com/conduitio/conduit/pkg/provisioning/config"

// PluginStatus is a connector/processor's --resolve-plugins verdict, as
// shown on the enriched-graph output. Left empty ("") when
// --resolve-plugins was off — never guess at a status for a ref that wasn't
// actually checked.
type PluginStatus string

const (
	// PluginStatusBuiltin: an explicit "builtin:<name>" ref that resolved to
	// a known builtin plugin.
	PluginStatusBuiltin PluginStatus = "builtin"
	// PluginStatusNotFound: an explicit "builtin:<name>" ref whose name is
	// not a known builtin. A connector/processor with this status also has
	// a corresponding error Finding (see plugins.go's
	// pluginNotFoundFinding) — it is repeated here purely for the enriched
	// graph's own readability, not as a second source of truth for pass/fail.
	PluginStatusNotFound PluginStatus = "not_found"
	// PluginStatusUnverified: a standalone or unprefixed ("any") ref —
	// `--resolve-plugins` only ever checks builtins (see the design doc's
	// failure modes), so this is never treated as a failure.
	PluginStatusUnverified PluginStatus = "unverified"
)

// EnrichedDLQ is a pipeline's DLQ configuration after config.Enrich has
// injected its defaults (pkg/pipeline.DefaultDLQ) for any field the config
// left unset.
type EnrichedDLQ struct {
	Plugin              string `json:"plugin"`
	WindowSize          int    `json:"windowSize"`
	WindowNackThreshold int    `json:"windowNackThreshold"`
}

// EnrichedProcessor is one processor after enrichment: its final
// (parentID-prefixed) ID, plugin, and worker count (defaulted to 1 by
// config.Enrich when unset).
type EnrichedProcessor struct {
	ID      string `json:"id"`
	Plugin  string `json:"plugin"`
	Workers int    `json:"workers"`
	// PluginStatus is set only when RunDryRun's resolvePlugins is true.
	PluginStatus PluginStatus `json:"pluginStatus,omitempty"`
}

// EnrichedConnector is one connector after enrichment: its final
// (pipelineID-prefixed) ID, type, and plugin, plus its own nested processors
// (each already enriched the same way).
type EnrichedConnector struct {
	ID         string              `json:"id"`
	Type       string              `json:"type"`
	Plugin     string              `json:"plugin"`
	Processors []EnrichedProcessor `json:"processors,omitempty"`
	// PluginStatus is set only when RunDryRun's resolvePlugins is true.
	PluginStatus PluginStatus `json:"pluginStatus,omitempty"`
}

// EnrichedPipeline is one pipeline's enriched view, as `dry-run` reports it:
// the same config.Enrich defaulting `conduit run`'s provisioning path
// applies before validation (final IDs, DLQ defaults, worker counts), so
// what `dry-run` shows is exactly what would be provisioned.
type EnrichedPipeline struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Status     string              `json:"status"`
	Connectors []EnrichedConnector `json:"connectors,omitempty"`
	Processors []EnrichedProcessor `json:"processors,omitempty"`
	DLQ        EnrichedDLQ         `json:"dlq"`
}

// EnrichedFile pairs one resolved file's path with the enriched view of
// every pipeline it contains — only the successfully parsed-and-enriched
// ones; a file with parse errors has none.
type EnrichedFile struct {
	Path      string             `json:"path"`
	Pipelines []EnrichedPipeline `json:"pipelines"`
}

// DryRunReport is RunDryRun's return value: the same Report every verb
// produces (Summary + per-file Findings), plus the enriched graph for every
// file.
type DryRunReport struct {
	Report
	Enriched []EnrichedFile
}

// DryRunResult is the --json envelope's `result` payload for `pipeline
// dry-run`: the same `files`/`findings` shape as validate/lint, plus
// `enriched`.
type DryRunResult struct {
	Files    []FileReport   `json:"files"`
	Enriched []EnrichedFile `json:"enriched"`
}

// buildEnrichedFiles converts the engine's working fileState slice into the
// enriched-graph view RunDryRun reports, resolving plugin refs into a
// PluginStatus per connector/processor when resolvePlugins is true (left
// empty otherwise — a ref that was never checked gets no verdict).
func buildEnrichedFiles(frs []fileState, resolvePlugins bool) []EnrichedFile {
	files := make([]EnrichedFile, len(frs))
	for i, fr := range frs {
		pipelines := make([]EnrichedPipeline, len(fr.pipelines))
		for j, p := range fr.pipelines {
			pipelines[j] = buildEnrichedPipeline(p, resolvePlugins)
		}
		files[i] = EnrichedFile{Path: fr.path, Pipelines: pipelines}
	}
	return files
}

func buildEnrichedPipeline(p config.Pipeline, resolvePlugins bool) EnrichedPipeline {
	connectors := make([]EnrichedConnector, len(p.Connectors))
	for i, c := range p.Connectors {
		connectors[i] = EnrichedConnector{
			ID:         c.ID,
			Type:       c.Type,
			Plugin:     c.Plugin,
			Processors: buildEnrichedProcessors(c.Processors, resolvePlugins),
		}
		if resolvePlugins {
			connectors[i].PluginStatus = pluginStatus(c.Plugin, builtinConnectorNames)
		}
	}

	return EnrichedPipeline{
		ID:         p.ID,
		Name:       p.Name,
		Status:     p.Status,
		Connectors: connectors,
		Processors: buildEnrichedProcessors(p.Processors, resolvePlugins),
		DLQ:        buildEnrichedDLQ(p.DLQ),
	}
}

func buildEnrichedProcessors(procs []config.Processor, resolvePlugins bool) []EnrichedProcessor {
	if len(procs) == 0 {
		return nil
	}
	out := make([]EnrichedProcessor, len(procs))
	for i, proc := range procs {
		out[i] = EnrichedProcessor{ID: proc.ID, Plugin: proc.Plugin, Workers: proc.Workers}
		if resolvePlugins {
			out[i].PluginStatus = pluginStatus(proc.Plugin, builtinProcessorNames)
		}
	}
	return out
}

func buildEnrichedDLQ(dlq config.DLQ) EnrichedDLQ {
	out := EnrichedDLQ{Plugin: dlq.Plugin}
	if dlq.WindowSize != nil {
		out.WindowSize = *dlq.WindowSize
	}
	if dlq.WindowNackThreshold != nil {
		out.WindowNackThreshold = *dlq.WindowNackThreshold
	}
	return out
}

// pluginStatus maps resolvePluginRef's verdict onto the enriched-graph's
// PluginStatus. An empty ref (already reported as a missing-field error by
// config.Validate) has no meaningful status either way, so it also reports
// unverified rather than a misleading "not_found".
func pluginStatus(ref string, builtinNames map[string]bool) PluginStatus {
	if ref == "" {
		return PluginStatusUnverified
	}
	switch resolvePluginRef(ref, builtinNames) {
	case pluginResolutionBuiltinOK:
		return PluginStatusBuiltin
	case pluginResolutionBuiltinNotFound:
		return PluginStatusNotFound
	case pluginResolutionUnverified:
		return PluginStatusUnverified
	default:
		return PluginStatusUnverified
	}
}
