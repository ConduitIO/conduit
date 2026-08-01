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
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

// This file is slice 3a of the arch-v2 multi-connector epic's chaos coverage:
// a SIGKILL scenario against funnel.Worker's M-destination fan-out
// (worker.go's doNextTask default case) and multiAckNacker, the highest-
// data-loss-risk code this slice adds. Unlike child.go/runChild (which reads
// and acks directly against pkg/connector.Source, proving the
// connector/persister durability boundary alone) and recovery_child.go's
// runChildLifecycle (which recovers a single-destination funnel.Worker
// through pkg/lifecycle-poc.Service's restart loop), this file drives a REAL
// funnel.Worker fanned out to TWO destinations, so a SIGKILL against this
// child process can land with a record durably written to a SUBSET of the M
// destinations before multiAckNacker's unanimous-ack gate lets Source.Ack
// see it (AC-8's exact scenario). It reuses buildChild's real
// *connector.Source (same chaosPlugin/badger-DB durability already proven
// gap-free by sigkill_test.go) and recovery_child.go's deliveryLog (a
// crash-durable, fsync'd, one-file-per-position ledger) for both
// destinations, so the parent test can read - directly from disk, after
// killing this process - exactly which positions each destination durably
// received, independent of anything this process's memory held.
const (
	envFanoutMode         = "CONDUIT_CHAOS_FANOUT_MODE"
	envFanoutDBDir        = "CONDUIT_CHAOS_FANOUT_DB_DIR"
	envFanoutUpstreamDir  = "CONDUIT_CHAOS_FANOUT_UPSTREAM_DIR"
	envFanoutDestADir     = "CONDUIT_CHAOS_FANOUT_DEST_A_DIR"
	envFanoutDestBDir     = "CONDUIT_CHAOS_FANOUT_DEST_B_DIR"
	envFanoutDLQDir       = "CONDUIT_CHAOS_FANOUT_DLQ_DIR"
	envFanoutTotal        = "CONDUIT_CHAOS_FANOUT_TOTAL"
	envFanoutPaceMS       = "CONDUIT_CHAOS_FANOUT_PACE_MS"
	envFanoutDestBDelayMS = "CONDUIT_CHAOS_FANOUT_DEST_B_DELAY_MS"

	// markerFanoutDone is the graceful "ran to total and stopped cleanly"
	// completion marker for this scenario - printed only by the restart run
	// that is allowed to finish (the first run is always SIGKILLed, which by
	// definition never prints anything more).
	markerFanoutDone = "FANOUT_DONE"
)

// fanoutChildConfig is the parent-side (harness) counterpart to
// fanoutChildEnv, mirroring childConfig's/lcChildConfig's env-building role.
// Kept as its own small protocol (like lcChildConfig) rather than added to
// childConfig's fields: this scenario's shape (one source, TWO independently
// durable destination ledgers, an artificial destination-speed asymmetry) is
// specific to the fan-out slice and doesn't fit the single-destination
// childConfig without distorting it.
type fanoutChildConfig struct {
	dbDir       string
	upstreamDir string
	destADir    string
	destBDir    string
	dlqDir      string

	total  uint64
	paceMS int

	// destBDelayMS makes destB's Write artificially slower than destA's,
	// so a kill timed on read/write progress is very likely to land with a
	// record durably in destA's ledger but not yet in destB's - the "subset
	// of the M destinations" precondition AC-8 requires. Without this
	// asymmetry, both branches would tend to complete each record at nearly
	// the same instant and the scenario this test exists to exercise would
	// rarely actually occur.
	destBDelayMS int
}

func (c fanoutChildConfig) env() []string {
	return []string{
		envChild + "=1", // isChildInvocation's gate, shared with every other child mode
		envFanoutMode + "=" + envValueTrue,
		envFanoutDBDir + "=" + c.dbDir,
		envFanoutUpstreamDir + "=" + c.upstreamDir,
		envFanoutDestADir + "=" + c.destADir,
		envFanoutDestBDir + "=" + c.destBDir,
		envFanoutDLQDir + "=" + c.dlqDir,
		envFanoutTotal + "=" + strconv.FormatUint(c.total, 10),
		envFanoutPaceMS + "=" + strconv.Itoa(c.paceMS),
		envFanoutDestBDelayMS + "=" + strconv.Itoa(c.destBDelayMS),
	}
}

// fanoutChildEnv is the child-side parsed form of fanoutChildConfig.
type fanoutChildEnv struct {
	dbDir       string
	upstreamDir string
	destADir    string
	destBDir    string
	dlqDir      string

	total  uint64
	paceMS int

	destBDelayMS int
}

// parseFanoutChildEnv reads and validates this child's environment. Like
// parseChildEnv/parseLCChildEnv, any failure here is a test-harness
// misconfiguration, not a scenario under test, so it exits immediately.
func parseFanoutChildEnv() fanoutChildEnv {
	var cfg fanoutChildEnv
	cfg.dbDir = os.Getenv(envFanoutDBDir)
	cfg.upstreamDir = os.Getenv(envFanoutUpstreamDir)
	cfg.destADir = os.Getenv(envFanoutDestADir)
	cfg.destBDir = os.Getenv(envFanoutDestBDir)
	cfg.dlqDir = os.Getenv(envFanoutDLQDir)

	total, err := strconv.ParseUint(os.Getenv(envFanoutTotal), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envFanoutTotal, err)
		os.Exit(exitBadArgs)
	}
	cfg.total = total

	paceMS, err := strconv.Atoi(os.Getenv(envFanoutPaceMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envFanoutPaceMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMS = paceMS

	destBDelayMS, err := strconv.Atoi(os.Getenv(envFanoutDestBDelayMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envFanoutDestBDelayMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.destBDelayMS = destBDelayMS

	if cfg.dbDir == "" || cfg.upstreamDir == "" || cfg.destADir == "" || cfg.destBDir == "" || cfg.dlqDir == "" {
		fmt.Fprintf(os.Stderr, "%s: %s, %s, %s, %s and %s are required\n",
			markerFatal, envFanoutDBDir, envFanoutUpstreamDir, envFanoutDestADir, envFanoutDestBDir, envFanoutDLQDir)
		os.Exit(exitBadArgs)
	}
	return cfg
}

// fanoutDestination adapts a deliveryLog (recovery_child.go) into a
// funnel.Destination for this scenario. Write durably records each record's
// (sourceID, position) via deliveryLog.Record (an fsync'd, one-file-per-
// delivery ledger that survives this process being SIGKILLed - see
// deliveryLog's doc comment), optionally after an artificial delay (see
// fanoutChildConfig.destBDelayMS's doc for why). sourceID is read off each
// record's metadataSourceKey (upstream.go's makeRecord/chaosPlugin.sourceTag)
// - "" if absent, which is every scenario except the N-source
// shared-destination collision case (nsource_child.go): slice 3a's fan-OUT
// topology (one source, M destinations, each with its own deliveryLog) has
// nothing to disambiguate, so its records never carry the salt and this is a
// no-op there. Ack immediately acks everything just written, matching every
// other synthetic destination in this package (synthDestination,
// property4_test.go): DestinationTask.Do's polling contract is one Write
// followed by repeated Ack calls until every written position has been
// acknowledged.
type fanoutDestination struct {
	id    string
	log   *deliveryLog
	delay time.Duration

	mu      sync.Mutex
	pending []connector.DestinationAck
}

func (d *fanoutDestination) ID() string                     { return d.id }
func (d *fanoutDestination) Open(context.Context) error     { return nil }
func (d *fanoutDestination) Teardown(context.Context) error { return nil }
func (d *fanoutDestination) Errors() <-chan error           { return make(chan error) }

func (d *fanoutDestination) Write(_ context.Context, recs []opencdc.Record) error {
	if d.delay > 0 {
		time.Sleep(d.delay)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		pos, err := decodePosition(r.Position)
		if err != nil {
			return fmt.Errorf("fanoutDestination %s: decode position %s: %w", d.id, r.Position, err)
		}
		sourceID := r.Metadata[metadataSourceKey] // "" if this record carries no salt (see type doc)
		if err := d.log.Record(sourceID, pos); err != nil {
			return fmt.Errorf("fanoutDestination %s: %w", d.id, err)
		}
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *fanoutDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

// runChildFanout is the entire child-process program for this scenario: a
// real *connector.Source (via buildChild, byte-for-byte the same
// construction every other chaos child uses) wrapped in a real funnel.Worker
// fanned out to two fanoutDestination branches (destA, destB), driving
// worker.go's multi-destination doNextTask path and multiAckNacker for real.
// It never returns until the run either completes (graceful, prints
// markerFanoutDone) or is killed out from under it (the SIGKILL case this
// scenario exists for - no cleanup on that path is possible or intended,
// exactly like runChild's own doc comment).
func runChildFanout() {
	ctx := context.Background()
	cfg := parseFanoutChildEnv()

	built, err := buildChild(ctx, childEnv{
		dbDir:       cfg.dbDir,
		upstreamDir: cfg.upstreamDir,
		paceMS:      cfg.paceMS,
		total:       cfg.total,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	printResumePosition(built.instance)

	logA, err := openDeliveryLog(cfg.destADir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	logB, err := openDeliveryLog(cfg.destBDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	dlqLog, err := openDeliveryLog(cfg.dlqDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	destA := &fanoutDestination{id: "fanout-dest-a", log: logA}
	destB := &fanoutDestination{id: "fanout-dest-b", log: logB, delay: time.Duration(cfg.destBDelayMS) * time.Millisecond}
	dlqDest := &fanoutDestination{id: "fanout-dlq-dest", log: dlqLog}

	logger := built.logger
	srcTask := funnel.NewSourceTask("fanout-source-task", built.src, logger, funnel.NoOpConnectorMetrics{})
	destATask := funnel.NewDestinationTask("fanout-dest-a-task", destA, logger, funnel.NoOpConnectorMetrics{})
	destBTask := funnel.NewDestinationTask("fanout-dest-b-task", destB, logger, funnel.NoOpConnectorMetrics{})

	destANode := &funnel.TaskNode{Task: destATask}
	destBNode := &funnel.TaskNode{Task: destBTask}
	// The fan-out point: the source task's Next holds BOTH destination
	// branches, exactly what service.go's buildTaskNodes wires up in
	// production for M destination connectors - see worker.go's doNextTask
	// default case and multiAckNacker.
	srcNode := &funnel.TaskNode{Task: srcTask, Next: []*funnel.TaskNode{destANode, destBNode}}

	// windowSize=0 disables the DLQ nack-threshold window outright (every
	// nack would be routed to the DLQ) - irrelevant here since this
	// scenario's happy-path pipeline never nacks anything, but a real
	// funnel.DLQ still has to be wired in (funnel.NewWorker requires one).
	dlq := funnel.NewDLQ("fanout-dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, 0, 0)

	worker, err := funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: build worker: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	if err := worker.Open(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: open worker: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	doErr := make(chan error, 1)
	go func() { doErr <- worker.Do(ctx) }()

	// Run until the upstream has committed every position (i.e. multiAckNacker
	// released every position to Source.Ack, which - per Source.Ack's own
	// invariant-1 durability ordering - drives chaosPlugin's upstream commit),
	// then stop gracefully. If this process is SIGKILLed before that happens
	// (the actual chaos), none of this is ever reached - exactly the point.
	//
	// Deliberately NOT child.go's waitForUpstreamCommitted: that helper's
	// hardcoded 5-second timeout is sized for the case it was built for -
	// runChild calls it only after runReadAckLoop has ALREADY read+acked
	// every record up to total, so it's waiting out a single last async
	// commit, not the whole run. Here the worker is still actively
	// reading/fanning-out/acking while this waits, and destB's deliberate
	// per-write delay (destBDelayMS) alone can add multiple seconds on a
	// resumed run under -race - reusing that 5s budget as the PRIMARY
	// completion wait intermittently fired it and force-exited this process
	// (confirmed: a `-race -count=10` run of the parent test produced 9
	// spurious "timed out waiting for upstream to reach position N" FATALs
	// before this fix). This loop's timeout is sized for this scenario
	// instead, and also fails loudly (rather than hanging) if the worker's
	// Do goroutine ever exits with an error while waiting.
	waitFanoutUpstreamCommitted(built.upstream, cfg.total, doErr)

	if err := worker.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stop worker: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := <-doErr; err != nil {
		fmt.Fprintf(os.Stderr, "%s: worker.Do returned an error: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := worker.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: close worker: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	built.persister.WaitPendingWrites()

	fmt.Println(markerFanoutDone)
	os.Exit(exitOK)
}

// waitFanoutUpstreamCommitted polls until the upstream store reports total
// positions committed (see this function's call site for why child.go's
// waitForUpstreamCommitted - a 5-second budget sized for waiting out one
// already-in-flight last commit - is the wrong tool here). It also drains
// doErr non-blockingly on every iteration: if the worker's Do goroutine
// exits on its own before reaching total, that is always an unexpected
// failure at this point in the run (nothing calls Stop before this
// function returns), so it is reported and this process exits immediately
// instead of spinning until the timeout with a misleading message.
//
// doErr is read via a non-blocking select (never a blocking receive), so on
// the success path (the common case) this function returns without having
// consumed doErr at all, leaving its one buffered value for runChildFanout's
// own `<-doErr` read after Stop - reading a buffered channel of capacity 1
// twice would deadlock the second read.
func waitFanoutUpstreamCommitted(upstream *upstreamStore, total uint64, doErr <-chan error) {
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
