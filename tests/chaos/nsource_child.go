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

package chaos

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/rs/zerolog"
)

// This file is slice 3b of the arch-v2 multi-connector epic's chaos
// coverage: a SIGKILL scenario against TWO independent source connectors
// (one fast, one deliberately slower) sharing ONE destination, driven
// through a real funnel.Sink and two real funnel.Worker instances - the
// exact production shape lifecycle-poc.Service.buildRunnablePipeline wires
// up for N sources. Unlike fanout_child.go (slice 3a: one source fanned out
// to M destinations), this is the reverse topology: it exercises
// TaskNode.MarkSharedBoundary's serialization of the shared destination
// against concurrent writers, and proves that a SIGKILL can land with EITHER
// source ahead of the other without losing or duplicating-with-a-gap either
// one's contribution to the single shared destination's ledger.
//
// It reuses buildChild's per-source construction pattern (a real
// *connector.Source backed by a real on-disk badger DB and
// connector.Persister, driven by an in-process chaosPlugin) TWICE - once per
// source, each with its own DB/upstream dir so each source's durable
// position is independently resumable - and fanoutDestination
// (fanout_child.go) for the single shared destination, backed by one
// deliveryLog so the parent test can read exactly which positions from
// EITHER source were durably written, independent of anything this
// process's memory held.
const (
	envNSourceMode         = "CONDUIT_CHAOS_NSOURCE_MODE"
	envNSourceDBDirA       = "CONDUIT_CHAOS_NSOURCE_DB_DIR_A"
	envNSourceDBDirB       = "CONDUIT_CHAOS_NSOURCE_DB_DIR_B"
	envNSourceUpstreamDirA = "CONDUIT_CHAOS_NSOURCE_UPSTREAM_DIR_A"
	envNSourceUpstreamDirB = "CONDUIT_CHAOS_NSOURCE_UPSTREAM_DIR_B"
	envNSourceDestDir      = "CONDUIT_CHAOS_NSOURCE_DEST_DIR"
	envNSourceDLQDirA      = "CONDUIT_CHAOS_NSOURCE_DLQ_DIR_A"
	envNSourceDLQDirB      = "CONDUIT_CHAOS_NSOURCE_DLQ_DIR_B"
	envNSourceTotalA       = "CONDUIT_CHAOS_NSOURCE_TOTAL_A"
	envNSourceTotalB       = "CONDUIT_CHAOS_NSOURCE_TOTAL_B"
	envNSourcePaceMSA      = "CONDUIT_CHAOS_NSOURCE_PACE_MS_A"
	envNSourcePaceMSB      = "CONDUIT_CHAOS_NSOURCE_PACE_MS_B"

	nsourceInstanceIDA = "chaos-nsource-a"
	nsourceInstanceIDB = "chaos-nsource-b"
	nsourcePluginA     = "chaos-nsource-plugin-a"
	nsourcePluginB     = "chaos-nsource-plugin-b"

	// nsourcePosOffsetB gives source B its own disjoint position namespace
	// ([nsourcePosOffsetB+1, nsourcePosOffsetB+totalB]) so its contributions
	// to the ONE shared destination ledger can never collide in value with
	// source A's ([1, totalA]) - see buildNSourceChildSource's doc. Source A
	// keeps offset 0 (its original, pre-3b-test position space).
	nsourcePosOffsetB = 1_000_000

	// markerNSourceDone is the graceful "both sources ran to their own total
	// and stopped cleanly" completion marker - printed only by the restart
	// run that is allowed to finish (the first run is always SIGKILLed).
	markerNSourceDone = "NSOURCE_DONE"
)

// nsourceChildConfig is the parent-side (harness) counterpart to
// nsourceChildEnv, mirroring fanoutChildConfig's role for slice 3a.
type nsourceChildConfig struct {
	dbDirA, dbDirB             string
	upstreamDirA, upstreamDirB string
	destDir                    string
	dlqDirA, dlqDirB           string

	totalA, totalB   uint64
	paceMSA, paceMSB int
}

func (c nsourceChildConfig) env() []string {
	return []string{
		envChild + "=1", // isChildInvocation's gate, shared with every other child mode
		envNSourceMode + "=" + envValueTrue,
		envNSourceDBDirA + "=" + c.dbDirA,
		envNSourceDBDirB + "=" + c.dbDirB,
		envNSourceUpstreamDirA + "=" + c.upstreamDirA,
		envNSourceUpstreamDirB + "=" + c.upstreamDirB,
		envNSourceDestDir + "=" + c.destDir,
		envNSourceDLQDirA + "=" + c.dlqDirA,
		envNSourceDLQDirB + "=" + c.dlqDirB,
		envNSourceTotalA + "=" + strconv.FormatUint(c.totalA, 10),
		envNSourceTotalB + "=" + strconv.FormatUint(c.totalB, 10),
		envNSourcePaceMSA + "=" + strconv.Itoa(c.paceMSA),
		envNSourcePaceMSB + "=" + strconv.Itoa(c.paceMSB),
	}
}

// nsourceChildEnv is the child-side parsed form of nsourceChildConfig.
type nsourceChildEnv struct {
	dbDirA, dbDirB             string
	upstreamDirA, upstreamDirB string
	destDir                    string
	dlqDirA, dlqDirB           string

	totalA, totalB   uint64
	paceMSA, paceMSB int
}

// parseNSourceChildEnv reads and validates this child's environment. Like
// parseChildEnv/parseFanoutChildEnv, any failure here is a test-harness
// misconfiguration, not a scenario under test, so it exits immediately.
func parseNSourceChildEnv() nsourceChildEnv {
	var cfg nsourceChildEnv
	cfg.dbDirA = os.Getenv(envNSourceDBDirA)
	cfg.dbDirB = os.Getenv(envNSourceDBDirB)
	cfg.upstreamDirA = os.Getenv(envNSourceUpstreamDirA)
	cfg.upstreamDirB = os.Getenv(envNSourceUpstreamDirB)
	cfg.destDir = os.Getenv(envNSourceDestDir)
	cfg.dlqDirA = os.Getenv(envNSourceDLQDirA)
	cfg.dlqDirB = os.Getenv(envNSourceDLQDirB)

	var err error
	cfg.totalA, err = strconv.ParseUint(os.Getenv(envNSourceTotalA), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNSourceTotalA, err)
		os.Exit(exitBadArgs)
	}
	cfg.totalB, err = strconv.ParseUint(os.Getenv(envNSourceTotalB), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNSourceTotalB, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMSA, err = strconv.Atoi(os.Getenv(envNSourcePaceMSA))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNSourcePaceMSA, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMSB, err = strconv.Atoi(os.Getenv(envNSourcePaceMSB))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNSourcePaceMSB, err)
		os.Exit(exitBadArgs)
	}

	if cfg.dbDirA == "" || cfg.dbDirB == "" || cfg.upstreamDirA == "" || cfg.upstreamDirB == "" ||
		cfg.destDir == "" || cfg.dlqDirA == "" || cfg.dlqDirB == "" {
		fmt.Fprintf(os.Stderr, "%s: all nsource dirs are required\n", markerFatal)
		os.Exit(exitBadArgs)
	}
	return cfg
}

// nsourceChildSource bundles one source's real engine pieces, mirroring
// childBuilt (child.go) but parameterized by instance ID/plugin name so this
// file can build TWO of them in one process - childBuilt/buildChild are
// hardwired to the single package-level `instanceID`/`instancePlugin`
// constants, which only fits a single-source scenario.
type nsourceChildSource struct {
	persister *connector.Persister
	upstream  *upstreamStore
	src       *connector.Source
}

// buildNSourceChildSource opens (or resumes) one source's on-disk badger DB
// and upstreamStore, and constructs a real *connector.Source around an
// in-process chaosPlugin configured with the given total/paceMS - the same
// construction buildChild uses, just parameterized for a specific
// instanceID/plugin name so two independent instances can coexist in one
// process.
//
// posOffset gives this source its own disjoint numeric position namespace:
// chaosPlugin.makeRecord (upstream.go) is a pure function of the position
// number alone (deterministic "key-%d"/"record-%d" content, no per-instance
// salt), so two chaosPlugin instances both counting 1..total would produce
// byte-for-byte IDENTICAL records at the same position - indistinguishable
// once they land in the ONE shared deliveryLog this scenario's destination
// writes into (see fanoutDestination.Write/deliveryLog.Record, which key
// purely on the numeric position). Seeding a fresh instance's resume
// position at posOffset (and bounding chaosPlugin.total at posOffset+total,
// since produceLoop compares its position counter to total as an ABSOLUTE
// value, not a relative count - see produceLoop's own doc) makes this
// source's positions occupy [posOffset+1, posOffset+total], disjoint from
// any other source using a different posOffset, so the parent test can tell
// the two sources' contributions to the shared ledger apart and verify each
// is independently gapless. posOffset is only applied when creating a FRESH
// instance; a resumed instance's persisted position already reflects
// whatever offset the original run seeded, so it is left untouched.
func buildNSourceChildSource(ctx context.Context, dbDir, upstreamDir, instanceID, pluginName string, posOffset, total uint64, paceMS int) (*nsourceChildSource, error) {
	logger := log.New(zerolog.Nop()) // keep stdout clean; it is our progress-line channel

	db, err := badger.New(zerolog.Nop(), dbDir)
	if err != nil {
		return nil, fmt.Errorf("open badger db (%s): %w", instanceID, err)
	}
	persister := connector.NewPersister(logger, db, connector.DefaultPersisterDelayThreshold, connector.DefaultPersisterBundleCountThreshold)
	store := connector.NewStore(db, logger)

	instance, err := store.Get(ctx, instanceID)
	switch {
	case err == nil:
		// resumed from a previous (possibly killed) run's persisted state -
		// its position already reflects whatever posOffset the very first
		// run for this instance seeded; do not touch it.
	case cerrors.Is(err, database.ErrKeyNotExist):
		instance = &connector.Instance{
			ID:            instanceID,
			Type:          connector.TypeSource,
			Config:        connector.Config{Name: instanceID, Settings: map[string]string{}},
			PipelineID:    instancePipe,
			Plugin:        pluginName,
			ProvisionedBy: connector.ProvisionTypeAPI,
			State:         connector.SourceState{Position: encodePosition(posOffset)},
		}
	default:
		fmt.Printf("%s: %v\n", markerCorruptPo, err)
		os.Exit(exitCorruptState)
		return nil, nil // unreachable
	}
	instance.Init(logger, persister)

	upstream, err := openUpstreamStore(upstreamDir, false)
	if err != nil {
		return nil, fmt.Errorf("open upstream store (%s): %w", instanceID, err)
	}
	plugin := &chaosPlugin{store: upstream, total: posOffset + total, paceMS: paceMS}

	fetcher := staticFetcher{pluginName: staticDispenser{source: plugin}}
	c, err := instance.Connector(ctx, fetcher)
	if err != nil {
		return nil, fmt.Errorf("build connector (%s): %w", instanceID, err)
	}
	src, ok := c.(*connector.Source)
	if !ok {
		return nil, fmt.Errorf("unexpected connector type %T (%s)", c, instanceID)
	}

	return &nsourceChildSource{persister: persister, upstream: upstream, src: src}, nil
}

// runChildNSource is the entire child-process program for this scenario: TWO
// real *connector.Source instances (via buildNSourceChildSource - the same
// construction every other chaos child uses, just parameterized), each
// wrapped in its own real funnel.Worker, both converging on ONE shared
// fanoutDestination (fanout_child.go) via a real funnel.Sink - the exact
// production shape lifecycle-poc.Service.buildRunnablePipeline wires up for
// N sources. It never returns until the run either completes (graceful,
// prints markerNSourceDone) or is killed out from under it (the SIGKILL case
// this scenario exists for - no cleanup on that path is possible or
// intended, exactly like runChild's own doc comment).
func runChildNSource() {
	ctx := context.Background()
	cfg := parseNSourceChildEnv()

	builtA, err := buildNSourceChildSource(ctx, cfg.dbDirA, cfg.upstreamDirA, nsourceInstanceIDA, nsourcePluginA, 0, cfg.totalA, cfg.paceMSA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	builtB, err := buildNSourceChildSource(ctx, cfg.dbDirB, cfg.upstreamDirB, nsourceInstanceIDB, nsourcePluginB, nsourcePosOffsetB, cfg.totalB, cfg.paceMSB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	destLog, err := openDeliveryLog(cfg.destDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	dlqLogA, err := openDeliveryLog(cfg.dlqDirA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	dlqLogB, err := openDeliveryLog(cfg.dlqDirB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	logger := log.New(zerolog.Nop())

	// ONE shared destination: both sources' workers converge on this exact
	// object via the shared TaskNode Sink wraps below.
	sharedDest := &fanoutDestination{id: "nsource-shared-dest", log: destLog}
	sharedDestTask := funnel.NewDestinationTask("nsource-shared-dest-task", sharedDest, logger, funnel.NoOpConnectorMetrics{})
	sharedRoot := &funnel.TaskNode{Task: sharedDestTask}

	sink, err := funnel.NewSink(sharedRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: build sink: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	buildWorker := func(built *nsourceChildSource, id string, dlqDest *fanoutDestination) (*funnel.Worker, error) {
		srcTask := funnel.NewSourceTask(id+"-src-task", built.src, logger, funnel.NoOpConnectorMetrics{})
		srcNode := &funnel.TaskNode{Task: srcTask}
		if err := srcNode.AppendToEnd(sharedRoot); err != nil {
			return nil, fmt.Errorf("attach shared sink for %s: %w", id, err)
		}
		// windowSize=0 disables the DLQ nack-threshold window outright (every
		// nack would be routed to the DLQ) - irrelevant here since this
		// scenario's happy-path pipeline never nacks anything, but a real
		// funnel.DLQ still has to be wired in (funnel.NewWorker requires one)
		// and it must be per-source (slice 3b: DLQ is never shared).
		dlq := funnel.NewDLQ(id+"-dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, 0, 0)
		return funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	}

	dlqDestA := &fanoutDestination{id: "nsource-dlq-a", log: dlqLogA}
	dlqDestB := &fanoutDestination{id: "nsource-dlq-b", log: dlqLogB}

	workerA, err := buildWorker(builtA, "nsource-a", dlqDestA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: build worker A: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	workerB, err := buildWorker(builtB, "nsource-b", dlqDestB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: build worker B: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	if err := sink.Open(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: open shared sink: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := workerA.Open(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: open worker A: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := workerB.Open(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: open worker B: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- workerA.Do(ctx) }()
	go func() { doErrB <- workerB.Do(ctx) }()

	// Run until BOTH upstreams have committed every position (each source's
	// own independent watermark), then stop gracefully. If this process is
	// SIGKILLed before that happens (the actual chaos), none of this is ever
	// reached - exactly the point.
	//
	// Deliberately NOT child.go's waitForUpstreamCommitted: that helper's
	// hardcoded 5-second timeout is sized for waiting out a single
	// already-in-flight last commit AFTER a read/ack loop already caught up
	// to total - see waitFanoutUpstreamCommitted's identical doc comment in
	// fanout_child.go for why reusing that budget here (while both workers
	// are still actively reading/writing/acking) is the wrong tool.
	waitNSourceUpstreamCommitted(builtA.upstream, cfg.totalA, doErrA)
	waitNSourceUpstreamCommitted(builtB.upstream, nsourcePosOffsetB+cfg.totalB, doErrB)

	if err := workerA.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stop worker A: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := workerB.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stop worker B: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := <-doErrA; err != nil {
		fmt.Fprintf(os.Stderr, "%s: worker A.Do returned an error: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := <-doErrB; err != nil {
		fmt.Fprintf(os.Stderr, "%s: worker B.Do returned an error: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	// Invariant (crux of slice 3b): each worker's own Close only tears down
	// its own source - the shared destination is untouched here.
	if err := workerA.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: close worker A: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := workerB.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: close worker B: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	// Only now, after BOTH workers have exited, is it safe to close the
	// shared sink - mirroring runPipeline's workersWg.Wait-gated
	// rp.sink.Close call.
	if err := sink.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: close shared sink: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	builtA.persister.WaitPendingWrites()
	builtB.persister.WaitPendingWrites()

	fmt.Println(markerNSourceDone)
	os.Exit(exitOK)
}

// waitNSourceUpstreamCommitted polls until upstream reports total positions
// committed (see this function's call site for why child.go's
// waitForUpstreamCommitted's 5-second budget is the wrong tool here — it is
// sized for waiting out one already-in-flight commit, not a whole active
// run). Mirrors fanout_child.go's waitFanoutUpstreamCommitted: doErr is
// drained non-blockingly on every iteration, so an unexpected early exit of
// the worker's Do goroutine is reported immediately instead of spinning
// until the timeout with a misleading message, and (on the success path) is
// left un-consumed for the caller's own later `<-doErr` read after Stop.
func waitNSourceUpstreamCommitted(upstream *upstreamStore, total uint64, doErr <-chan error) {
	const timeout = 25 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		committed, err := upstream.Committed()
		if err == nil && committed >= total {
			return
		}

		select {
		case doneErr := <-doErr:
			fmt.Fprintf(os.Stderr, "%s: worker.Do exited early (err=%v) before reaching upstream position %d\n", markerFatal, doneErr, total)
			os.Exit(exitOpenOtherError)
		default:
		}

		time.Sleep(time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "%s: timed out after %s waiting for upstream to reach position %d\n", markerFatal, timeout, total)
	os.Exit(exitOpenOtherError)
}
