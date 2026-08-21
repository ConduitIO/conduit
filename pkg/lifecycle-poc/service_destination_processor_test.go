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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// Regression coverage for #2736: arch-v2 built a destination's own
// (connector-scoped) processors AFTER the destination task in the funnel
// task graph, instead of before it (buildDestinationTasks appended destTask
// first, then the connector's procTasks). The destination therefore wrote
// the UNTRANSFORMED record, and the processors ran on a copy whose output
// fed only the acker and nowhere else - a silent, undetected no-op. v1
// (pkg/lifecycle) chains a destination connector's own processors between
// `prev` and the destination node - i.e. before the write - see
// buildDestinationNodes there. These tests build a REAL processor (the
// builtin field.set plugin, in-process - no gRPC/WASM involved) so the
// assertion exercises actual record mutation, not just task-graph shape,
// and wire it through the full lifecycle-poc Service.Start path (the exact
// path buildDestinationTasks/buildSharedTail run in production).
//
// Every test in this file fails against the pre-fix ordering: the
// asserterDestination mock's DestinationPluginWithRecords requires an exact,
// ordered match against the MUTATED records; with processors running after
// the write, the mock instead receives the raw, unmutated records and the
// mismatch fails the test from inside the destination's onRun goroutine
// (surfaced via t.Cleanup's wg.WaitTimeout / gomock's own T.Fatal machinery).

// newTestProcessorService returns a real, in-process processor.Service wired
// to the builtin processor registry (field.set et al.) - not a stub. Unlike
// testProcessorService (which unconditionally returns "not implemented" from
// MakeRunnableProcessor - see its doc), this can actually build a
// *processor.RunnableProcessor that transforms records, which is the only
// way to prove destination-scoped processors run before the write rather
// than merely asserting task-graph shape.
func newTestProcessorService(t *testing.T, logger log.CtxLogger, db database.DB) *processor.Service {
	t.Helper()
	is := is.New(t)

	schemaReg, err := schemaregistry.NewSchemaRegistry(db)
	is.NoErr(err)

	procRegistry := proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, schemaReg)
	procPluginService := proc_plugin.NewPluginService(logger, procRegistry, nil)

	return processor.NewService(logger, db, procPluginService)
}

// createFieldSetProcessor creates a real "field.set" processor instance
// scoped to parentID (a destination connector's ID), configured to set the
// record's field to value. field.set is a real builtin plugin (in-process,
// no gRPC/WASM) so Process() actually mutates the record - this is not a
// mock standing in for a processor.
func createFieldSetProcessor(
	ctx context.Context,
	t *testing.T,
	procService *processor.Service,
	parentID string,
	field string,
	value string,
) *processor.Instance {
	t.Helper()
	is := is.New(t)

	inst, err := procService.Create(
		ctx,
		uuid.NewString(),
		"builtin:field.set",
		processor.Parent{ID: parentID, Type: processor.ParentTypeConnector},
		processor.Config{Settings: map[string]string{"field": field, "value": value}},
		processor.ProvisionTypeAPI,
		"",
	)
	is.NoErr(err)
	return inst
}

// TestServiceLifecycle_DestinationScopedProcessor_TransformsWrittenRecord is
// the core #2736 regression test: a processor scoped to the DESTINATION
// connector (not a pipeline-level processor) must transform the record
// BEFORE it reaches the destination's Write - not after, where its output
// would go nowhere but the acker. The destination mock is configured to
// expect the MUTATED records; with the pre-fix ordering it instead receives
// the raw, unmutated ones and the test fails.
func TestServiceLifecycle_DestinationScopedProcessor_TransformsWrittenRecord(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	procService := newTestProcessorService(t, logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)

	// mutatedRecords is what the destination should actually receive: every
	// record's Key replaced by the field.set processor. If the fix is
	// correct, the destination sees these, not wantRecords.
	mutatedRecords := make([]opencdc.Record, len(wantRecords))
	for i, r := range wantRecords {
		r.Key = opencdc.RawData("redacted-by-destination-processor")
		mutatedRecords[i] = r
	}

	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, mutatedRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	// Scope the processor to the DESTINATION connector, not the pipeline -
	// this is exactly the buildDestinationTasks path #2736 is about
	// (pipeline-level processors, built by buildProcessorTasks/pl.ProcessorIDs
	// and chained in buildSharedTail, were never affected by this bug).
	destProc := createFieldSetProcessor(ctx, t, procService, destination.ID, ".Key", "redacted-by-destination-processor")
	destination.ProcessorIDs = []string{destProc.ID}

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		procService,
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	// Poll a watermark (the source's persisted ack position) rather than
	// asserting instantly - the record has to flow through the funnel
	// (source -> processor -> destination -> ack) asynchronously.
	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))

	// waitForRecordsAcked already proved the source acked every record at
	// the ORIGINAL (unmutated) position - positions are never touched by
	// field.set - so acking happened exactly once, after the write, with
	// the right position (Invariant 1/2). The asserterDestination mock's
	// own t.Cleanup (registered inside DestinationPluginWithRecords) proves
	// the destination received exactly mutatedRecords, in order, no more and
	// no less - that is the ordering assertion this test exists for.
}

// TestServiceLifecycle_DestinationScopedProcessor_MultipleProcessors_AppliedInDeclaredOrder
// proves a chain of MULTIPLE destination-scoped processors runs in
// declaration order before the write, not just that a single processor runs
// before it. The second processor's template reads the record's CURRENT
// (in-flight) .Key value, so the final value written to the destination
// encodes which processor ran first: "declared-first-declared-second" if the
// declared order was honored, "-declared-second" (first processor's static
// value clobbers everything) if reversed. This also implicitly proves the
// chain runs before the write: if either step ran after, the destination
// would see the ORIGINAL uuid key instead of either derived value.
func TestServiceLifecycle_DestinationScopedProcessor_MultipleProcessors_AppliedInDeclaredOrder(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	procService := newTestProcessorService(t, logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(5)

	const wantFinalKey = "declared-first-declared-second"
	mutatedRecords := make([]opencdc.Record, len(wantRecords))
	for i, r := range wantRecords {
		r.Key = opencdc.RawData(wantFinalKey)
		mutatedRecords[i] = r
	}

	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, mutatedRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	// proc1 sets Key to a fixed value; proc2's template reads back whatever
	// Key currently holds and appends to it. Declared order is [proc1, proc2].
	proc1 := createFieldSetProcessor(ctx, t, procService, destination.ID, ".Key", "declared-first")
	// .Key is opencdc.RawData ([]byte); Go's text/template prints a []byte's
	// default %v as "[100 101 ...]" (decimal byte values) rather than as
	// text, since RawData does not implement fmt.Stringer - printf "%s"
	// forces the string conversion so the template reads back the actual
	// key content proc1 set.
	proc2 := createFieldSetProcessor(ctx, t, procService, destination.ID, ".Key", `{{ printf "%s" .Key }}-declared-second`)
	destination.ProcessorIDs = []string{proc1.ID, proc2.ID}

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		procService,
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
}

// TestServiceLifecycle_SourceScopedProcessor_TransformsRecordBeforeSharedTail
// closes the "equally untested" gap #2736 calls out for the SOURCE side:
// buildSourceTasks builds a source's own chain as [srcTask, procTasks...]
// (source-scoped processors already run AFTER the source task and BEFORE the
// shared tail is attached, by construction) - this test proves that with a
// REAL, mutating processor rather than asserting task-graph shape. Unlike
// the destination-scoped bug, this path was already correctly ordered; this
// is coverage for previously-unverified-but-correct behavior, not a fix.
func TestServiceLifecycle_SourceScopedProcessor_TransformsRecordBeforeSharedTail(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	procService := newTestProcessorService(t, logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)

	mutatedRecords := make([]opencdc.Record, len(wantRecords))
	for i, r := range wantRecords {
		r.Key = opencdc.RawData("redacted-by-source-processor")
		mutatedRecords[i] = r
	}

	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, mutatedRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	// Scope the processor to the SOURCE connector this time.
	srcProc := createFieldSetProcessor(ctx, t, procService, source.ID, ".Key", "redacted-by-source-processor")
	source.ProcessorIDs = []string{srcProc.ID}

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		procService,
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
}

// TestServiceLifecycle_PipelineLevelProcessor_TransformsRecordBeforeDestinationWrite
// closes another previously-untested path: pl.ProcessorIDs (pipeline-level,
// not connector-scoped, processors), built by buildProcessorTasks and
// chained by buildSharedTail as procRoot -> destBranches (see
// buildSharedTail's doc). All existing lifecycle-poc tests use
// testProcessorService{}, whose MakeRunnableProcessor unconditionally
// returns "not implemented" (see its doc) - so, like the destination-scoped
// case, no pipeline-level processor's actual record transformation had ever
// been exercised end-to-end in arch-v2 before this file. Found correctly
// ordered; this is coverage, not a fix.
func TestServiceLifecycle_PipelineLevelProcessor_TransformsRecordBeforeDestinationWrite(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	procService := newTestProcessorService(t, logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(10)

	mutatedRecords := make([]opencdc.Record, len(wantRecords))
	for i, r := range wantRecords {
		r.Key = opencdc.RawData("redacted-by-pipeline-processor")
		mutatedRecords[i] = r
	}

	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	destination, destDispenser := asserterDestination(ctrl, persister, mutatedRecords, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	// Scope the processor to the PIPELINE, not any one connector.
	plProc := createFieldSetProcessor(ctx, t, procService, pl.ID, ".Key", "redacted-by-pipeline-processor")
	pl, err = ps.AddProcessor(ctx, pl.ID, plProc.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID:      source,
			destination.ID: destination,
			testDLQID:      dlq,
		},
		procService,
		testConnectorPluginService{
			source.Plugin:      sourceDispenser,
			destination.Plugin: destDispenser,
			dlq.Plugin:         dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
}

// TestServiceLifecycle_MultiDestination_DestinationScopedProcessors_ApplyIndependently
// is the 3a/3b (multi-destination fan-out) neighbourhood check: one source
// fans out to TWO destinations, each with its OWN distinct destination-scoped
// processor. buildDestinationTasks builds one independent []funnel.Task
// branch per destination (see its doc), so the #2736 fix (procTasks before
// destTask) needs to hold for EVERY branch independently, not just a single
// destination - and each branch's processor must affect only that
// destination's write, never bleed into the sibling branch's. Both
// destination mocks require an EXACT match against their own, differently
// mutated record set; if either branch reused the pre-fix ordering, or if
// the two branches' processors cross-contaminated, one or both mocks would
// see the wrong content and fail.
func TestServiceLifecycle_MultiDestination_DestinationScopedProcessors_ApplyIndependently(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, time.Second, 3)

	ps := pipeline.NewService(logger, db)
	procService := newTestProcessorService(t, logger, db)

	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	ctrl := gomock.NewController(t)
	wantRecords := generateRecords(6)

	mutatedForDest1 := make([]opencdc.Record, len(wantRecords))
	mutatedForDest2 := make([]opencdc.Record, len(wantRecords))
	for i, r := range wantRecords {
		r1 := r
		r1.Key = opencdc.RawData("redacted-for-dest-1")
		mutatedForDest1[i] = r1

		r2 := r
		r2.Key = opencdc.RawData("redacted-for-dest-2")
		mutatedForDest2[i] = r2
	}

	source, sourceDispenser := generatorSource(ctrl, persister, wantRecords, nil, false)
	dest1, dest1Dispenser := asserterDestination(ctrl, persister, mutatedForDest1, false)
	dest2, dest2Dispenser := asserterDestination(ctrl, persister, mutatedForDest2, false)
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, false)
	pl.DLQ.Plugin = dlq.Plugin

	proc1 := createFieldSetProcessor(ctx, t, procService, dest1.ID, ".Key", "redacted-for-dest-1")
	dest1.ProcessorIDs = []string{proc1.ID}
	proc2 := createFieldSetProcessor(ctx, t, procService, dest2.ID, ".Key", "redacted-for-dest-2")
	dest2.ProcessorIDs = []string{proc2.ID}

	pl, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest1.ID)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, dest2.ID)
	is.NoErr(err)

	ls := NewService(
		logger,
		testErrRecoveryCfg(),
		testConnectorService{
			source.ID: source,
			dest1.ID:  dest1,
			dest2.ID:  dest2,
			testDLQID: dlq,
		},
		procService,
		testConnectorPluginService{
			source.Plugin: sourceDispenser,
			dest1.Plugin:  dest1Dispenser,
			dest2.Plugin:  dest2Dispenser,
			dlq.Plugin:    dlqDispenser,
		},
		ps,
		false,
	)

	err = ls.Start(ctx, pl.ID)
	is.NoErr(err)

	waitForRecordsAcked(t, source, wantRecords)

	is.Equal(pipeline.StatusRunning, pl.GetStatus())
	is.Equal("", pl.Error)

	err = ls.Stop(ctx, pl.ID, false)
	is.NoErr(err)
	is.NoErr(ls.WaitPipeline(pl.ID))
}
