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

package lifecycle

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/starthook"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// passthroughProcessorPlugins is a processor plugin service whose every
// plugin passes records through unchanged. A "standalone:" prefix resolves to
// standalone, and so does an unprefixed name once shadow is set to it (a
// standalone plugin of that name installed). built counts the processors it
// has built.
type passthroughProcessorPlugins struct {
	built  atomic.Int32
	shadow atomic.Pointer[string]
}

func (p *passthroughProcessorPlugins) NewProcessor(_ context.Context, name string, _ string, _ egress.Policy) (sdk.Processor, error) {
	p.built.Add(1)
	return sdk.NewProcessorFunc(sdk.Specification{Name: name}, func(_ context.Context, r opencdc.Record) (opencdc.Record, error) {
		return r, nil
	}), nil
}

func (p *passthroughProcessorPlugins) ResolvesToStandalone(name string) bool {
	if sh := p.shadow.Load(); sh != nil && *sh == name {
		return true
	}
	return strings.HasPrefix(name, "standalone:")
}

const (
	encodePlugin = "builtin:debezium.encode"
	wasmPlugin   = "standalone:my-wasm"
	fieldPlugin  = "builtin:field.set"
)

// boundaryPlacement places processors on a one-source, one-destination
// pipeline: plugin references in execution order on each part.
type boundaryPlacement struct {
	source, pipeline, dest []string
	sourcePlugin           string // overrides the source connector's plugin
	sourceErr              error  // the source fails with it after its records
}

type boundaryHarness struct {
	ls      *Service
	pl      *pipeline.Instance
	source  *connector.Instance
	plugins *passthroughProcessorPlugins
	records []opencdc.Record
}

// newBoundaryHarness builds the pipeline. With run=false no connector may be
// dispensed (the start must be refused before anything opens).
func newBoundaryHarness(t *testing.T, place boundaryPlacement, run bool) *boundaryHarness {
	t.Helper()
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)
	t.Cleanup(func() { stopAndWaitPersister(t, cancel, persister) })
	ps := pipeline.NewService(logger, db)
	plugins := &passthroughProcessorPlugins{}
	procs := processor.NewService(logger, db, plugins)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "boundary-test"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	ctrl := gomock.NewController(t)

	h := &boundaryHarness{pl: pl, plugins: plugins, records: generateRecords(10)}
	var source, dest, dlq *connector.Instance
	var sourceDisp, destDisp, dlqDisp connectorPlugin.Dispenser
	if run {
		source, sourceDisp = generatorSource(ctrl, persister, h.records, place.sourceErr, false)
		dest, destDisp = asserterDestination(ctrl, persister, h.records, false)
		dlq, dlqDisp = asserterDestination(ctrl, persister, nil, false)
	} else {
		source, sourceDisp = dummySource(persister), untouchedDispenser(ctrl)
		dest, destDisp = dummyDestination(persister), untouchedDispenser(ctrl)
		dlq, dlqDisp = dummyDestination(persister), untouchedDispenser(ctrl)
	}
	if place.sourcePlugin != "" {
		source.Plugin = place.sourcePlugin
	}
	h.source = source

	create := func(plugin string, parent processor.Parent) string {
		inst, err := procs.Create(ctx, uuid.NewString(), plugin, parent, processor.Config{}, processor.ProvisionTypeAPI, "")
		is.NoErr(err)
		return inst.ID
	}
	for _, p := range place.source {
		source.ProcessorIDs = append(source.ProcessorIDs, create(p, processor.Parent{ID: source.ID, Type: processor.ParentTypeConnector}))
	}
	for _, p := range place.dest {
		dest.ProcessorIDs = append(dest.ProcessorIDs, create(p, processor.Parent{ID: dest.ID, Type: processor.ParentTypeConnector}))
	}
	for _, p := range place.pipeline {
		id := create(p, processor.Parent{ID: pl.ID, Type: processor.ParentTypePipeline})
		_, err = ps.AddProcessor(ctx, pl.ID, id)
		is.NoErr(err)
	}

	pl.DLQ.Plugin = dlq.Plugin
	_, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, dest.ID)
	is.NoErr(err)

	h.ls = NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, dest.ID: dest, testDLQID: dlq},
		procs,
		testConnectorPluginService{source.Plugin: sourceDisp, dest.Plugin: destDisp, dlq.Plugin: dlqDisp},
		ps, false)
	return h
}

// TestStart_RefusesStandaloneBeforeDebeziumEncode is #3124 (design
// 20261012-debezium-compatible-output.md, decision 18): a pipeline whose
// records can reach debezium.encode through a standalone plugin is refused at
// Start with the code, before any processor is built or connector opened.
func TestStart_RefusesStandaloneBeforeDebeziumEncode(t *testing.T) {
	for name, place := range map[string]boundaryPlacement{
		"pipeline processor before pipeline encoder":  {pipeline: []string{fieldPlugin, wasmPlugin, encodePlugin}},
		"source processor before destination encoder": {source: []string{wasmPlugin}, dest: []string{encodePlugin}},
		"destination processor before its encoder":    {dest: []string{wasmPlugin, encodePlugin}},
		"standalone source connector":                 {dest: []string{encodePlugin}, sourcePlugin: "standalone:postgres"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBoundaryHarness(t, place, false)
			builtAtCreate := h.plugins.built.Load()

			err := h.ls.Start(context.Background(), h.pl.ID)
			is.True(err != nil) // start must be refused
			ce, ok := conduiterr.Get(err)
			is.True(ok) // refusal must be coded
			is.Equal(ce.Code, starthook.CodeDebeziumStandaloneBeforeEncode)
			is.True(strings.Contains(ce.Message, h.pl.ID))
			is.True(strings.Contains(ce.Message, "standalone:")) // names the plugin
			is.True(strings.Contains(ce.Suggestion, "built-in"))

			// Nothing built, nothing running (untouched dispensers fail the
			// test if a connector was dispensed).
			is.Equal(h.plugins.built.Load(), builtAtCreate)
			is.True(h.pl.GetStatus() != pipeline.StatusRunning)
			_, running := h.ls.runningPipelines.Get(h.pl.ID)
			is.True(!running)
		})
	}
}

// TestStart_DebeziumEncodeOnBuiltinPathStarts: built-in plugins before the
// encoder, or a standalone processor after it or off its path, start and
// deliver every record.
func TestStart_DebeziumEncodeOnBuiltinPathStarts(t *testing.T) {
	for name, place := range map[string]boundaryPlacement{
		"all built-in":                        {source: []string{fieldPlugin}, pipeline: []string{fieldPlugin, encodePlugin}},
		"standalone processor after encoder":  {pipeline: []string{encodePlugin, wasmPlugin}},
		"standalone processors, no encoder":   {source: []string{wasmPlugin}, pipeline: []string{wasmPlugin}, dest: []string{wasmPlugin}},
		"encoder on destination, built-in in": {pipeline: []string{fieldPlugin}, dest: []string{encodePlugin}},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBoundaryHarness(t, place, true)

			is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
			waitForRecordsAcked(t, h.source, h.records)
			is.Equal(h.pl.GetStatus(), pipeline.StatusRunning)

			is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
			is.NoErr(h.ls.WaitPipeline(h.pl.ID))
		})
	}
}

// TestRecovery_RefusedRestartDegradesWithoutRetry: the check runs on the
// recovery restart too. If the pipeline's path into debezium.encode now
// resolves to a standalone plugin (here, one installed under the name of a
// built-in processor during the backoff), the restart is refused, the pipeline
// is degraded with the code, and nothing is retried: the source is dispensed
// once (a second dispense fails the mock).
func TestRecovery_RefusedRestartDegradesWithoutRetry(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newBoundaryHarness(t, boundaryPlacement{
		pipeline:  []string{"field.set", encodePlugin},
		sourceErr: cerrors.New("lost connection to source"),
	}, true)
	backoffs := atomic.Int32{}
	h.ls.testAfterBackoffWait = func(*runnablePipeline) {
		backoffs.Add(1)
		name := "field.set"
		h.plugins.shadow.Store(&name)
	}
	builtBefore := h.plugins.built.Load()

	is.NoErr(h.ls.Start(ctx, h.pl.ID))

	deadline := time.Now().Add(5 * time.Second)
	for h.pl.GetStatus() != pipeline.StatusDegraded {
		if time.Now().After(deadline) {
			t.Fatalf("pipeline status %v, want degraded", h.pl.GetStatus())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The run's terminal error is the refusal, with its code.
	ce, ok := conduiterr.Get(h.ls.WaitPipeline(h.pl.ID))
	is.True(ok)
	is.Equal(ce.Code, starthook.CodeDebeziumStandaloneBeforeEncode)
	time.Sleep(50 * time.Millisecond) // a retry would back off for at most 10ms
	is.Equal(backoffs.Load(), int32(1))
	// The first run built both processors; the refused restart built none.
	is.Equal(h.plugins.built.Load(), builtBefore+2)
}
