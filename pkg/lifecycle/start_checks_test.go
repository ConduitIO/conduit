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
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/starthook"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	conn_standalone "github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	procmock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
	proc_standalone "github.com/conduitio/conduit/pkg/plugin/processor/standalone"
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
	ls       *Service
	pl       *pipeline.Instance
	source   *connector.Instance
	plugins  *passthroughProcessorPlugins
	procs    *processor.Service
	pipeProc []string // pipeline processor IDs, in order
	records  []opencdc.Record
}

// newBoundaryHarness builds the pipeline (the classic engine's twin of
// pkg/lifecycle-poc/start_checks_test.go). With run=false no connector may be
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

	h := &boundaryHarness{pl: pl, plugins: plugins, procs: procs, records: generateRecords(10)}
	var source, dest, dlq *connector.Instance
	var sourceDisp, destDisp, dlqDisp connectorPlugin.Dispenser
	if run {
		source, sourceDisp = asserterSource(ctrl, persister, h.records, place.sourceErr, place.sourceErr == nil, 1)
		dest, destDisp = asserterDestination(ctrl, persister, h.records, 1)
		dlq, dlqDisp = asserterDestination(ctrl, persister, nil, 1)
	} else {
		// No expectations: any dispense fails the test.
		source, sourceDisp = dummySource(persister), pmock.NewDispenser(ctrl)
		dest, destDisp = dummyDestination(persister), pmock.NewDispenser(ctrl)
		dlq, dlqDisp = dummyDestination(persister), pmock.NewDispenser(ctrl)
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
		h.pipeProc = append(h.pipeProc, id)
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
		ps)
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

// waitForAck polls until source's persisted position is want.
func waitForAck(t *testing.T, source *connector.Instance, want opencdc.Position) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		source.RLock()
		state, ok := source.State.(connector.SourceState)
		source.RUnlock()
		if ok && bytes.Equal(state.Position, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("source did not ack position %q in time", want)
		}
		time.Sleep(10 * time.Millisecond)
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
			waitForAck(t, h.source, h.records[len(h.records)-1].Position)
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
	// The run is over: no live or backing-off run is left that could retry,
	// and only the one backoff happened.
	_, live := h.ls.runningPipelines.Get(h.pl.ID)
	is.True(!live)
	is.Equal(backoffs.Load(), int32(1))
	// The first run built both processors; the refused restart built none.
	is.Equal(h.plugins.built.Load(), builtBefore+2)
}

// TestReconfigureProcessor_RefusesStandaloneBeforeDebeziumEncode: the live
// in-place swap (provisioning's applyInPlace) runs the start checks too. A swap
// that puts a standalone plugin before debezium.encode is refused with the
// code before the new processor is built; the old processor keeps running and
// the pipeline keeps delivering.
func TestReconfigureProcessor_RefusesStandaloneBeforeDebeziumEncode(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newBoundaryHarness(t, boundaryPlacement{pipeline: []string{fieldPlugin, encodePlugin}}, true)

	is.NoErr(h.ls.Start(ctx, h.pl.ID))
	waitForAck(t, h.source, h.records[len(h.records)-1].Position)
	builtBefore := h.plugins.built.Load()

	// Provisioning persists the new config first, then asks for the swap.
	_, err := h.procs.UpdateWhileRunning(ctx, h.pipeProc[0], wasmPlugin, processor.Config{})
	is.NoErr(err)
	err = h.ls.ReconfigureProcessor(ctx, h.pl.ID, h.pipeProc[0])
	ce, ok := conduiterr.Get(err)
	is.True(ok) // refusal must be coded
	is.Equal(ce.Code, starthook.CodeDebeziumStandaloneBeforeEncode)
	is.True(strings.Contains(ce.Message, wasmPlugin))

	is.Equal(h.plugins.built.Load(), builtBefore) // nothing built
	is.Equal(h.pl.GetStatus(), pipeline.StatusRunning)

	is.NoErr(h.ls.Stop(ctx, h.pl.ID, false))
	is.NoErr(h.ls.WaitPipeline(h.pl.ID))
}

// TestStart_RealResolverChain resolves through the real services: the
// processor service and plugin service down to a standalone processor
// registry (empty here), and the connector plugin service down to a
// standalone connector registry loaded from a real plugin directory. The
// source references the installed standalone plugin without a type prefix
// and without a version, so only a lookup that sees the registry's latest
// entry finds it; the unprefixed built-in processor before the encoder must
// not be named, because no standalone processor of that name is installed.
func TestStart_RealResolverChain(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procStandalone, err := proc_standalone.NewRegistry(logger, t.TempDir(), schema.NewInMemoryService())
	is.NoErr(err)
	procs := processor.NewService(logger, db, proc_plugin.NewPluginService(logger, procmock.NewRegistry(ctrl), procStandalone))

	connStandalone := conn_standalone.NewRegistry(logger, "../plugin/connector/standalone/test")
	connStandalone.Init(ctx, ":12345", server.DefaultMaxReceiveRecordSize)
	connPlugins := connectorPlugin.NewPluginService(logger, pmock.NewBuiltinReg(ctrl), connStandalone, pmock.NewAuthManager(ctrl))

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "real-chain"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	persister := connector.NewPersister(logger, db, time.Second, 3)
	source := dummySource(persister)
	source.Plugin = "test-plugin" // installed in the standalone registry dir
	dest := dummyDestination(persister)
	stored := testProcessorService{
		"field": {ID: "field", Plugin: "field.set"},
		"enc":   {ID: "enc", Plugin: encodePlugin},
	}
	_, err = ps.AddProcessor(ctx, pl.ID, "field")
	is.NoErr(err)
	_, err = ps.AddProcessor(ctx, pl.ID, "enc")
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, dest.ID)
	is.NoErr(err)

	err = starthook.Check(ctx, pl, starthook.Services{
		Connectors:       testConnectorService{source.ID: source, dest.ID: dest},
		Processors:       realResolverProcessors{testProcessorService: stored, real: procs},
		ConnectorPlugins: connPlugins,
	})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, starthook.CodeDebeziumStandaloneBeforeEncode)
	is.True(strings.Contains(ce.Message, `uses standalone plugin "test-plugin"`))
	is.True(!strings.Contains(ce.Message, `"field"`))

	// The same source with a builtin: prefix is not standalone.
	source.Plugin = "builtin:test-plugin"
	is.NoErr(starthook.Check(ctx, pl, starthook.Services{
		Connectors:       testConnectorService{source.ID: source, dest.ID: dest},
		Processors:       realResolverProcessors{testProcessorService: stored, real: procs},
		ConnectorPlugins: connPlugins,
	}))
}

// realResolverProcessors serves stored instances from a map (the encoder is
// not a registered built-in yet, so the real service could not create it) and
// resolves plugins through the real processor.Service.
type realResolverProcessors struct {
	testProcessorService
	real *processor.Service
}

func (r realResolverProcessors) ResolvesToStandalone(p string) bool {
	return r.real.ResolvesToStandalone(p)
}
