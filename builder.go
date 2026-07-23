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

package conduit

import (
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
)

// Pipeline/connector field-value constants, re-exported so a builder-only
// caller never needs to import provisioning/config directly just to spell
// "running" or "source" correctly. Each is the same value as
// provisioning/config's own constant (validate.go) — not a new one — so a
// literal string and the constant are always interchangeable.
const (
	// StatusRunning is a pipeline's default desired status, applied by
	// Engine.Import's enrichment when WithStatus is never called.
	StatusRunning = provisioningconfig.StatusRunning
	// StatusStopped marks a pipeline as provisioned but not started.
	StatusStopped = provisioningconfig.StatusStopped

	// ConnectorTypeSource marks a connector as a pipeline source.
	ConnectorTypeSource = provisioningconfig.TypeSource
	// ConnectorTypeDestination marks a connector as a pipeline destination.
	ConnectorTypeDestination = provisioningconfig.TypeDestination
)

// PipelineBuilder builds a PipelineConfig field by field, producing exactly
// the struct a YAML parse of the equivalent pipeline document produces — see
// PipelineConfig's doc for why that identity matters. Obtain one with
// NewPipeline; chain With... calls; terminate with Build.
//
// # Not safe for concurrent or repeated use
//
// A PipelineBuilder — and the ConnectorBuilder/ProcessorBuilder/DLQBuilder
// below — is a single-owner, write-once value: build it inline, attach it
// to its parent (WithConnector/WithProcessor/WithDLQ), and let it go. Each
// With... method on the parent snapshots the child's state at the moment
// it's attached, so a nested builder is safe to construct as a chained
// expression passed directly as an argument, but mutating it *after*
// attaching it does not retroactively change the pipeline it was already
// attached to. None of these types are safe for concurrent use from
// multiple goroutines.
//
// Passing a nil *ConnectorBuilder/*ProcessorBuilder/*DLQBuilder to a With...
// method panics on the nil pointer dereference inside it, the same as any
// other Go API that dereferences a caller-supplied pointer — there is no
// nil guard here, matching the normal, unavoidable-in-practice pattern of
// constructing a nested builder inline as the With... call's argument
// (e.g. WithConnector(NewSourceConnector(...))), which can never be nil.
type PipelineBuilder struct {
	pipeline provisioningconfig.Pipeline
}

// NewPipeline starts a PipelineBuilder for a pipeline with the given id. id
// is required; Build returns a coded error if it's ever empty when called.
func NewPipeline(id string) *PipelineBuilder {
	return &PipelineBuilder{pipeline: provisioningconfig.Pipeline{ID: id}}
}

// WithName sets the pipeline's display name. Leave unset to have
// Engine.Import's enrichment default it to the pipeline's id.
func (b *PipelineBuilder) WithName(name string) *PipelineBuilder {
	b.pipeline.Name = name
	return b
}

// WithDescription sets the pipeline's human-readable description.
func (b *PipelineBuilder) WithDescription(description string) *PipelineBuilder {
	b.pipeline.Description = description
	return b
}

// WithStatus sets the pipeline's desired status: StatusRunning or
// StatusStopped. Leave unset to have Engine.Import's enrichment default it
// to StatusRunning.
func (b *PipelineBuilder) WithStatus(status string) *PipelineBuilder {
	b.pipeline.Status = status
	return b
}

// WithConnector appends a connector built by NewSourceConnector or
// NewDestinationConnector. Call order is preserved.
func (b *PipelineBuilder) WithConnector(c *ConnectorBuilder) *PipelineBuilder {
	b.pipeline.Connectors = append(b.pipeline.Connectors, c.build())
	return b
}

// WithProcessor appends a pipeline-scoped processor — one that runs on every
// connector's records, as opposed to a processor scoped to a single
// connector via ConnectorBuilder.WithProcessor. Call order is preserved.
func (b *PipelineBuilder) WithProcessor(p *ProcessorBuilder) *PipelineBuilder {
	b.pipeline.Processors = append(b.pipeline.Processors, p.build())
	return b
}

// WithDLQ sets the pipeline's dead-letter-queue configuration. Omitting this
// call leaves DLQ at its zero value — matching a YAML pipeline with no
// dead-letter-queue block — and Engine.Import's enrichment fills in
// Conduit's default DLQ plugin/settings/window at import time, exactly as it
// does for a parsed YAML pipeline that also omits the block. Calling
// WithDLQ a second time replaces the previous value rather than merging.
func (b *PipelineBuilder) WithDLQ(d *DLQBuilder) *PipelineBuilder {
	b.pipeline.DLQ = d.build()
	return b
}

// Build validates the pipeline and returns the resulting PipelineConfig.
//
// Validation runs Validate against an enriched copy of the pipeline — the
// same config.Enrich-then-config.Validate sequence `conduit pipelines
// validate` runs on a parsed YAML document — so a Build-time error catches
// the same class of mistake the CLI would catch (missing id/plugin/type,
// an out-of-range name/description, a negative worker count, a duplicate
// connector/processor id, ...). The returned PipelineConfig, however, is the
// raw, unenriched value this builder constructed: final namespaced IDs and
// injected DLQ defaults are Engine.Import's job at import time, not
// Build's — which is what keeps a hand-built PipelineConfig
// indistinguishable from a parsed one, the property this package's
// round-trip tests assert.
//
// A validation failure is returned as the same coded, config-path-scoped
// *conduiterr.ConduitError provisioning/config.Validate already produces for
// the CLI and API — never a panic, and never a new error code minted by
// this package.
func (b *PipelineBuilder) Build() (PipelineConfig, error) {
	enriched := provisioningconfig.Enrich(b.pipeline)
	if err := provisioningconfig.Validate(enriched); err != nil {
		return PipelineConfig{}, err
	}
	return b.pipeline, nil
}

// ConnectorBuilder builds one connector (source or destination) to attach to
// a PipelineBuilder. Obtain one with NewSourceConnector or
// NewDestinationConnector — there is deliberately no bare NewConnector
// constructor, so a connector's type can never be left empty or misspelled.
type ConnectorBuilder struct {
	connector provisioningconfig.Connector
}

func newConnector(connType, id, plugin string) *ConnectorBuilder {
	return &ConnectorBuilder{connector: provisioningconfig.Connector{
		ID:     id,
		Type:   connType,
		Plugin: plugin,
	}}
}

// NewSourceConnector starts a ConnectorBuilder for a source connector with
// the given id and plugin (e.g. "builtin:postgres").
func NewSourceConnector(id, plugin string) *ConnectorBuilder {
	return newConnector(ConnectorTypeSource, id, plugin)
}

// NewDestinationConnector starts a ConnectorBuilder for a destination
// connector with the given id and plugin.
func NewDestinationConnector(id, plugin string) *ConnectorBuilder {
	return newConnector(ConnectorTypeDestination, id, plugin)
}

// WithName sets the connector's display name. Leave unset to have
// Engine.Import's enrichment default it to the connector's id.
func (c *ConnectorBuilder) WithName(name string) *ConnectorBuilder {
	c.connector.Name = name
	return c
}

// WithSetting sets a single connector configuration key. A later call with
// the same key overwrites the earlier value.
func (c *ConnectorBuilder) WithSetting(key, value string) *ConnectorBuilder {
	if c.connector.Settings == nil {
		c.connector.Settings = make(map[string]string)
	}
	c.connector.Settings[key] = value
	return c
}

// WithSettings merges settings into the connector's configuration — the same
// as calling WithSetting for every entry. Keys already set and not present
// in settings are left untouched.
func (c *ConnectorBuilder) WithSettings(settings map[string]string) *ConnectorBuilder {
	for k, v := range settings {
		c.WithSetting(k, v)
	}
	return c
}

// WithProcessor appends a processor scoped to this connector only. Call
// order is preserved.
func (c *ConnectorBuilder) WithProcessor(p *ProcessorBuilder) *ConnectorBuilder {
	c.connector.Processors = append(c.connector.Processors, p.build())
	return c
}

func (c *ConnectorBuilder) build() provisioningconfig.Connector {
	return c.connector
}

// ProcessorBuilder builds one processor. The same ProcessorBuilder type
// attaches at pipeline scope (PipelineBuilder.WithProcessor) or connector
// scope (ConnectorBuilder.WithProcessor) — config.Processor carries no scope
// field of its own; where a built processor is attached is what determines
// its scope.
type ProcessorBuilder struct {
	processor provisioningconfig.Processor
}

// NewProcessor starts a ProcessorBuilder with the given id and plugin (e.g.
// "js", "builtin:field.rename"). There is deliberately no WithType:
// config.Processor has no Type field — "type" is a deprecated YAML-only wire
// name for what this package (and config.Processor) always calls Plugin,
// mechanically renamed by the parser's own linter (provisioning/config/
// yaml/v2's Changelog) — the builder never exposes the deprecated spelling.
func NewProcessor(id, plugin string) *ProcessorBuilder {
	return &ProcessorBuilder{processor: provisioningconfig.Processor{ID: id, Plugin: plugin}}
}

// WithSetting sets a single processor configuration key. A later call with
// the same key overwrites the earlier value.
func (p *ProcessorBuilder) WithSetting(key, value string) *ProcessorBuilder {
	if p.processor.Settings == nil {
		p.processor.Settings = make(map[string]string)
	}
	p.processor.Settings[key] = value
	return p
}

// WithSettings merges settings into the processor's configuration — the same
// as calling WithSetting for every entry. Keys already set and not present
// in settings are left untouched.
func (p *ProcessorBuilder) WithSettings(settings map[string]string) *ProcessorBuilder {
	for k, v := range settings {
		p.WithSetting(k, v)
	}
	return p
}

// WithWorkers sets the processor's worker count. Leave unset (0) to accept
// Engine.Import's enrichment default of 1. Invariant 4 (per-partition
// ordering): more than one worker can reorder records within a key, so raise
// this only deliberately.
func (p *ProcessorBuilder) WithWorkers(workers int) *ProcessorBuilder {
	p.processor.Workers = workers
	return p
}

// WithCondition sets a CEL expression gating whether this processor runs on
// a given record. Leave empty (the default) to run unconditionally.
func (p *ProcessorBuilder) WithCondition(condition string) *ProcessorBuilder {
	p.processor.Condition = condition
	return p
}

func (p *ProcessorBuilder) build() provisioningconfig.Processor {
	return p.processor
}

// DLQBuilder builds a pipeline's dead-letter-queue configuration. Obtain one
// with NewDLQ and attach it with PipelineBuilder.WithDLQ.
//
// WindowSize and WindowNackThreshold are *int fields on the underlying
// config.DLQ specifically so "never set" (nil, matching an omitted YAML
// window-size/window-nack-threshold key — Engine.Import's enrichment applies
// Conduit's own default) is distinguishable from "explicitly set to zero"
// (a non-nil pointer to 0). WithWindowSize/WithWindowNackThreshold are the
// only way to set either field, and only ever produce the explicit-value
// form — there is no way to construct the nil form other than never calling
// them, matching the enrichment path's own semantics exactly.
type DLQBuilder struct {
	dlq provisioningconfig.DLQ
}

// NewDLQ starts a DLQBuilder for the given DLQ plugin (e.g. "builtin:log").
func NewDLQ(plugin string) *DLQBuilder {
	return &DLQBuilder{dlq: provisioningconfig.DLQ{Plugin: plugin}}
}

// WithSetting sets a single DLQ configuration key. A later call with the
// same key overwrites the earlier value.
func (d *DLQBuilder) WithSetting(key, value string) *DLQBuilder {
	if d.dlq.Settings == nil {
		d.dlq.Settings = make(map[string]string)
	}
	d.dlq.Settings[key] = value
	return d
}

// WithSettings merges settings into the DLQ's configuration — the same as
// calling WithSetting for every entry. Keys already set and not present in
// settings are left untouched.
func (d *DLQBuilder) WithSettings(settings map[string]string) *DLQBuilder {
	for k, v := range settings {
		d.WithSetting(k, v)
	}
	return d
}

// WithWindowSize sets the DLQ's nack window size explicitly, including to 0
// — see DLQBuilder's doc on the nil-vs-explicit-zero distinction.
func (d *DLQBuilder) WithWindowSize(size int) *DLQBuilder {
	d.dlq.WindowSize = &size
	return d
}

// WithWindowNackThreshold sets the DLQ's nack threshold explicitly,
// including to 0 — see DLQBuilder's doc on the nil-vs-explicit-zero
// distinction.
func (d *DLQBuilder) WithWindowNackThreshold(threshold int) *DLQBuilder {
	d.dlq.WindowNackThreshold = &threshold
	return d
}

func (d *DLQBuilder) build() provisioningconfig.DLQ {
	return d.dlq
}
