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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/rs/zerolog"
)

// This file closes issue #2740: it is the end-to-end chaos coverage for H2
// (the adversarial-review finding on #2734, fixed by TaskNode.poisoned - see
// worker.go's doTask and CodeSharedDestinationPoisoned's doc comment), which
// until now was proven ONLY by an in-package unit test
// (pkg/lifecycle-poc/funnel/worker_h2_poison_test.go's
// TestNSource_H2_AckStreamErrorPoisonsSharedDestination) against a
// hand-built probe destination. That test never ran the fix through the real
// path: doTask's shared-boundary lock, DestinationTask.Do's early return on
// an Ack() error, real connector.Source/connector.Persister durability, or a
// real SIGKILL+resume. This file reuses nsource_child.go's real,
// production-shaped wiring (two real *connector.Source instances, each in
// its own funnel.Worker, converging on one shared funnel.Sink boundary via
// TaskNode.MarkSharedBoundary - exactly what
// lifecycle-poc.Service.buildRunnablePipeline wires for N sources) and
// #2739's colliding-position harness (nsourceSourceTagA/B,
// deliveryLog.PositionsBySource), and adds the one thing neither had: a
// destination that can actually trigger H2's failure mode.
//
// # The fault-injection mechanism, and why it faithfully reproduces "unread
// # acks left on a shared stream"
//
// h2FaultDestination below deliberately does NOT commit a write to the
// durable deliveryLog synchronously in Write, unlike fanoutDestination
// (fanout_child.go), which nsource_child.go's ordinary collision scenario
// uses. Write only buffers a record's (sourceID, position); the durable
// commit happens inside Ack, exactly once, at the moment a caller's Ack()
// call actually pops and returns that specific entry. This models a
// destination whose Write is merely a buffered send and whose Ack is the
// ONLY confirmation that a write is durably committed (batched inserts,
// buffered gRPC streams, etc.) - the shape that makes H2 a genuine
// invariant-1 hazard rather than a bookkeeping curiosity: if some OTHER
// caller's Ack() call pops and "confirms" an entry on a worker's behalf
// (byte-identical position, different origin), that worker can ack its own
// source upstream while its OWN write sits forever unread and uncommitted in
// the queue.
//
// Configured with failAt=1, h2FaultDestination's very first Ack() call
// returns an error WITHOUT popping the queue - modeling DestinationTask.Do
// returning early on an Ack() error with the write still queued, unread,
// on the shared stream (see destination.go's Do and PR #2734's adversarial
// review, finding H2). Source A always hits this on its first record; source
// B is held back (via chaosPlugin.startGate, gated on
// h2FaultDestination.triggered - closed the instant the fault fires) so it
// can only attempt to enter the shared destination AFTER the fault - and
// therefore the poison flag, if the fix holds - is already in place. B's own
// first record is given the SAME position value as A's (both sources count
// from 1 - see nsourceSourceTagA/B, nsource_child.go's doc), so if the
// poison check were ever bypassed, B's Ack() call would pop A's leftover,
// byte-match by pure coincidence of value, and B would ack its own source on
// the strength of a write it never made - the exact H2 symptom.
//
// # Why no SIGKILL lands mid-race
//
// The H2 race itself (doTask releasing sharedMu on A's error vs. a sibling
// acquiring it) is a microsecond-scale in-process goroutine race, not
// something an OS-level process kill can usefully time - gating B's
// production on h2FaultDestination.triggered already makes the race
// deterministic (B cannot even attempt entry before A's fault - and
// therefore the poison store, which happens strictly before sharedMu's
// deferred Unlock - has already happened; see worker.go's doTask comments on
// why that store/load pair is race-free by construction). What a SIGKILL
// DOES usefully exercise here is resume: this scenario's first child process
// runs the fault, captures and prints what both workers observed, snapshots
// the shared destination's ledger and both sources' upstream watermarks,
// then blocks forever - modeling a pipeline that has hit Degraded status and
// is waiting for an operator to restart it, exactly like
// recovery_child.go's crashable variant. The parent test SIGKILLs it (at a
// marker-gated, not wall-clock-gated, moment - see waitForMarker) and
// restarts an ORDINARY nsource child (runChildNSource, no fault injection)
// against the same on-disk state, proving the pipeline recovers cleanly and
// completes gaplessly despite the earlier poisoning.
const (
	envNSourceH2Mode = "CONDUIT_CHAOS_NSOURCE_H2_MODE"

	// markerH2ErrA/B carry worker A/B's Do() error text (sanitized to a
	// single line - see safeOneLine) - purely diagnostic, printed for every
	// run regardless of outcome.
	markerH2ErrA = "H2_ERR_A"
	markerH2ErrB = "H2_ERR_B"
	// markerH2ErrBCode carries worker B's error's conduiterr Code.Reason(),
	// or "NONE" if B's error (if any) wasn't a *conduiterr.ConduitError, or
	// if B's outcome was decided via the poison-bypass branch below instead
	// of an error at all. nsource_h2_sigkill_test.go asserts this equals
	// funnel.CodeSharedDestinationPoisoned.Reason() - the "poison error
	// surfaces rather than a silent success" requirement.
	markerH2ErrBCode = "H2_ERR_B_CODE"

	// markerH2PoisonBypassed is printed ONLY if worker B's pass was NOT
	// refused at the shared boundary - i.e. it reached Ack() on the
	// desynchronized stream. This line must NEVER appear with the H2 fix
	// intact; nsource_h2_sigkill_test.go asserts its absence. It exists
	// (and is exercised) purely by this workstream's non-vacuity
	// verification procedure - see that test's doc comment - which
	// temporarily neutralizes worker.go's poison check to confirm this
	// marker (and the invariant-1 violation it precedes) actually appears
	// when the fix is genuinely absent.
	markerH2PoisonBypassed = "H2_POISON_BYPASSED" //nolint:gosec // false positive: "BYPASSED" matches gosec's credential-name heuristic on "PASS"; this is a stdout progress-marker string, not a credential

	// markerH2Confirmed is printed once both workers' outcomes have been
	// captured, printed, and the resulting on-disk state has settled
	// (WaitPendingWrites, plus - only on the unreachable-under-the-fix
	// bypass path - a bounded poll for the wrongly-issued ack to finish
	// propagating). The parent test waits for this marker before SIGKILLing
	// the process - see waitForMarker's doc for why this is race-free
	// relative to wall-clock timing.
	markerH2Confirmed = "H2_FAULT_CONFIRMED"
)

// nsourceH2ChildConfig is the parent-side (harness) counterpart to
// nsourceH2ChildEnv, mirroring nsourceChildConfig's role for the ordinary
// collision scenario. totalA/totalB are deliberately small and asymmetric in
// how they're used (see runChildNSourceH2's doc): both workers stop after
// their very first record in THIS (fault) run by construction (A always
// errors on it; B either gets refused or - only when the fix is bypassed
// for verification - succeeds once and then blocks forever on its now-
// exhausted source, never reaching a second record that could race with
// this process's own snapshot-then-block sequence). The RESUME run reuses
// ordinary nsourceChildConfig/runChildNSource with its own, independently
// chosen totals - see nsource_h2_sigkill_test.go.
type nsourceH2ChildConfig struct {
	dbDirA, dbDirB             string
	upstreamDirA, upstreamDirB string
	destDir                    string
	dlqDirA, dlqDirB           string

	totalA, totalB uint64
	failAt         int
}

func (c nsourceH2ChildConfig) env() []string {
	return []string{
		envChild + "=1",
		envNSourceH2Mode + "=" + envValueTrue,
		envNSourceDBDirA + "=" + c.dbDirA,
		envNSourceDBDirB + "=" + c.dbDirB,
		envNSourceUpstreamDirA + "=" + c.upstreamDirA,
		envNSourceUpstreamDirB + "=" + c.upstreamDirB,
		envNSourceDestDir + "=" + c.destDir,
		envNSourceDLQDirA + "=" + c.dlqDirA,
		envNSourceDLQDirB + "=" + c.dlqDirB,
		envNSourceTotalA + "=" + strconv.FormatUint(c.totalA, 10),
		envNSourceTotalB + "=" + strconv.FormatUint(c.totalB, 10),
		envNSourceH2FailAt + "=" + strconv.Itoa(c.failAt),
	}
}

const envNSourceH2FailAt = "CONDUIT_CHAOS_NSOURCE_H2_FAIL_AT"

// nsourceH2ChildEnv is the child-side parsed form of nsourceH2ChildConfig.
type nsourceH2ChildEnv struct {
	dbDirA, dbDirB             string
	upstreamDirA, upstreamDirB string
	destDir                    string
	dlqDirA, dlqDirB           string

	totalA, totalB uint64
	failAt         int
}

func parseNSourceH2ChildEnv() nsourceH2ChildEnv {
	var cfg nsourceH2ChildEnv
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
	cfg.failAt, err = strconv.Atoi(os.Getenv(envNSourceH2FailAt))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNSourceH2FailAt, err)
		os.Exit(exitBadArgs)
	}

	if cfg.dbDirA == "" || cfg.dbDirB == "" || cfg.upstreamDirA == "" || cfg.upstreamDirB == "" ||
		cfg.destDir == "" || cfg.dlqDirA == "" || cfg.dlqDirB == "" {
		fmt.Fprintf(os.Stderr, "%s: all nsource H2 dirs are required\n", markerFatal)
		os.Exit(exitBadArgs)
	}
	return cfg
}

// h2PendingDelivery is one buffered, not-yet-durable write sitting in
// h2FaultDestination's FIFO queue - see the type's doc comment.
type h2PendingDelivery struct {
	sourceID string
	position uint64
	ack      connector.DestinationAck
}

// h2FaultDestination is this file's fault-injectable funnel.Destination -
// see the file doc comment for the full mechanism and why it faithfully
// reproduces H2's "unread acks left on a shared stream" hazard.
type h2FaultDestination struct {
	id  string
	log *deliveryLog

	failAt int // 1-indexed Ack() call number (across this destination's whole lifetime) that fails; 0 = never
	errAck error

	// triggered closes the instant the failAt-th Ack() call fires - before
	// returning the error. This is the ONLY synchronization this scenario
	// needs: source B's chaosPlugin.startGate is this channel, which is what
	// makes "B cannot attempt entry before A's fault" a structural
	// guarantee rather than a timing hope.
	triggered     chan struct{}
	triggeredOnce sync.Once

	// secondAckResolved closes the instant the SECOND-ever Ack() call
	// returns (successfully or not). With the H2 fix intact this call never
	// happens at all (a poisoned worker is refused before ever reaching
	// Write/Ack - see doTask) - this channel exists solely so
	// runChildNSourceH2 can detect the unreachable-under-the-fix case (used
	// only by this workstream's non-vacuity verification, never by the
	// shipped, poison-enabled path) without an open-ended wait on a
	// worker.Do() goroutine that, in that case, blocks forever on its
	// now-exhausted source (see the file doc comment).
	secondAckResolved     chan struct{}
	secondAckResolvedOnce sync.Once

	mu      sync.Mutex
	pending []h2PendingDelivery
	ackCall int
}

func newH2FaultDestination(id string, log *deliveryLog, failAt int, errAck error) *h2FaultDestination {
	return &h2FaultDestination{
		id:                id,
		log:               log,
		failAt:            failAt,
		errAck:            errAck,
		triggered:         make(chan struct{}),
		secondAckResolved: make(chan struct{}),
	}
}

func (d *h2FaultDestination) ID() string                     { return d.id }
func (d *h2FaultDestination) Open(context.Context) error     { return nil }
func (d *h2FaultDestination) Teardown(context.Context) error { return nil }
func (d *h2FaultDestination) Errors() <-chan error           { return make(chan error) }

// Write buffers each record's (sourceID, position) WITHOUT committing it to
// the durable deliveryLog - see the type doc for why deferring durability to
// a successful Ack() pop (not Write) is what makes an ack-stream desync
// capable of producing a genuine invariant-1 violation, unlike
// fanoutDestination (fanout_child.go), which commits synchronously in Write
// and therefore cannot reproduce this failure mode.
func (d *h2FaultDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		pos, err := decodePosition(r.Position)
		if err != nil {
			return fmt.Errorf("h2FaultDestination %s: decode position %s: %w", d.id, r.Position, err)
		}
		sourceID := r.Metadata[metadataSourceKey]
		d.pending = append(d.pending, h2PendingDelivery{
			sourceID: sourceID,
			position: pos,
			ack:      connector.DestinationAck{Position: r.Position},
		})
	}
	return nil
}

// Ack pops and durably commits the head of the pending FIFO queue - see the
// type doc. If this is the failAt-th call across this destination's whole
// lifetime, it returns an error WITHOUT popping: the head entry, and its
// NOT-YET-DURABLE write, stays queued for whichever caller reaches this
// destination next - H2's exact trigger ("unread acks left on the shared
// stream", PR #2734's adversarial review).
//
// Committing head.sourceID/head.position - the entry's OWN origin, never
// whatever the CALLER expected or claimed - is deliberate: this is the
// ground truth a post-hoc PositionsBySource() read checks the caller's
// behavior against. The funnel-level bug this scenario proves is fixed is
// that DestinationTask.Do's validateAcks only compares Position BYTES, never
// identity - so if doTask's poison check is ever bypassed, a caller can walk
// away believing an ack confirms ITS OWN write when the ledger says
// otherwise.
func (d *h2FaultDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.ackCall++
	if d.ackCall == d.failAt {
		d.triggeredOnce.Do(func() { close(d.triggered) })
		return nil, d.errAck
	}
	if d.ackCall == 2 {
		defer d.secondAckResolvedOnce.Do(func() { close(d.secondAckResolved) })
	}

	if len(d.pending) == 0 {
		return nil, cerrors.New("h2FaultDestination: Ack called with nothing pending")
	}
	head := d.pending[0]
	d.pending = d.pending[1:]

	if err := d.log.Record(head.sourceID, head.position); err != nil {
		return nil, fmt.Errorf("h2FaultDestination %s: %w", d.id, err)
	}
	return []connector.DestinationAck{head.ack}, nil
}

// safeOneLine collapses err's message to a single stdout line (this
// package's progress-marker protocol is line-based - see printProgressStr)
// and reports "<nil>" for a nil error, so H2_ERR_A/B always have a value.
func safeOneLine(err error) string {
	if err == nil {
		return "<nil>"
	}
	s := err.Error()
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// exitOnErr prints a markerFatal line naming what, and exits the process
// with code, if err is non-nil - it is a no-op otherwise. This is the exact
// "check a setup call, die loudly" pattern every child in this package
// already uses inline (see child.go/nsource_child.go); factored out here
// purely to keep runChildNSourceH2's cyclomatic complexity down given how
// many setup steps this scenario has - behavior is byte-for-byte identical
// to the inline form used elsewhere.
func exitOnErr(err error, code int, what string) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %s: %v\n", markerFatal, what, err)
	os.Exit(code)
}

// runChildNSourceH2 is this scenario's entire child-process program (see the
// file doc comment for the full design). It never returns: it either exits
// early on a setup failure (exitBadArgs/exitOpenOtherError, a harness bug,
// not the scenario under test - matching every other child in this package)
// or, on reaching the scenario's designed outcome, prints markerH2Confirmed
// and blocks forever for the parent to SIGKILL.
func runChildNSourceH2() {
	ctx := context.Background()
	cfg := parseNSourceH2ChildEnv()

	destLog, err := openDeliveryLog(cfg.destDir)
	exitOnErr(err, exitBadArgs, "open dest delivery log")
	dlqLogA, err := openDeliveryLog(cfg.dlqDirA)
	exitOnErr(err, exitBadArgs, "open DLQ A delivery log")
	dlqLogB, err := openDeliveryLog(cfg.dlqDirB)
	exitOnErr(err, exitBadArgs, "open DLQ B delivery log")

	logger := log.New(zerolog.Nop())

	ackErr := cerrors.New("chaos: injected H2 ack-stream fault - DestinationTask.Do's Ack() call " +
		"failed with unread acks still queued on the shared stream (see PR #2734's adversarial review, finding H2)")
	sharedDest := newH2FaultDestination("nsource-h2-shared-dest", destLog, cfg.failAt, ackErr)
	sharedDestTask := funnel.NewDestinationTask("nsource-h2-shared-dest-task", sharedDest, logger, funnel.NoOpConnectorMetrics{})
	sharedRoot := &funnel.TaskNode{Task: sharedDestTask}

	sink, err := funnel.NewSink(sharedRoot)
	exitOnErr(err, exitBadArgs, "build sink")

	// Source A is never gated: it always reaches the shared destination
	// first and triggers the fault on its very first Ack() call
	// (cfg.failAt=1 - see nsource_h2_sigkill_test.go). Source B is gated on
	// sharedDest.triggered - see the file doc comment for why this makes
	// the scenario fully deterministic.
	builtA, err := buildNSourceChildSource(ctx, cfg.dbDirA, cfg.upstreamDirA, nsourceInstanceIDA, nsourcePluginA, nsourceSourceTagA, cfg.totalA, 0, nil)
	exitOnErr(err, exitBadArgs, "build source A")
	builtB, err := buildNSourceChildSource(ctx, cfg.dbDirB, cfg.upstreamDirB, nsourceInstanceIDB, nsourcePluginB, nsourceSourceTagB, cfg.totalB, 0, sharedDest.triggered)
	exitOnErr(err, exitBadArgs, "build source B")

	dlqDestA := &fanoutDestination{id: "nsource-h2-dlq-a", log: dlqLogA}
	dlqDestB := &fanoutDestination{id: "nsource-h2-dlq-b", log: dlqLogB}

	buildWorker := func(built *nsourceChildSource, id string, dlqDest *fanoutDestination) (*funnel.Worker, error) {
		srcTask := funnel.NewSourceTask(id+"-src-task", built.src, logger, funnel.NoOpConnectorMetrics{})
		srcNode := &funnel.TaskNode{Task: srcTask}
		if err := srcNode.AppendToEnd(sharedRoot); err != nil {
			return nil, fmt.Errorf("attach shared sink for %s: %w", id, err)
		}
		dlq := funnel.NewDLQ(id+"-dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, 0, 0)
		return funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	}

	workerA, err := buildWorker(builtA, "nsource-h2-a", dlqDestA)
	exitOnErr(err, exitBadArgs, "build worker A")
	workerB, err := buildWorker(builtB, "nsource-h2-b", dlqDestB)
	exitOnErr(err, exitBadArgs, "build worker B")

	exitOnErr(sink.Open(ctx), exitOpenOtherError, "open shared sink")
	exitOnErr(workerA.Open(ctx), exitOpenOtherError, "open worker A")
	exitOnErr(workerB.Open(ctx), exitOpenOtherError, "open worker B")

	doErrA := make(chan error, 1)
	doErrB := make(chan error, 1)
	go func() { doErrA <- workerA.Do(ctx) }()
	go func() { doErrB <- workerB.Do(ctx) }()

	// A always hits the injected fault on its very first record, independent
	// of anything about B or the poison mechanism - this always resolves
	// quickly.
	errA := <-doErrA
	printProgressStr(markerH2ErrA, safeOneLine(errA))

	// B's outcome is one of exactly two shapes (see the file doc comment):
	//   - the fix holds: doTask refuses B entry outright (poisoned), B's
	//     Do() returns that error immediately - doErrB fires.
	//   - the fix is bypassed (unreachable in the shipped, poison-enabled
	//     code - see nsource_h2_sigkill_test.go's non-vacuity verification):
	//     B reaches Write/Ack, wrongly succeeds once, then blocks forever on
	//     its now-exhausted (cfg.totalB=1) source's next Read() - doErrB
	//     never fires, but sharedDest.secondAckResolved does, the instant
	//     that wrong ack has already been durably (mis)committed.
	var errB error
	var poisonBypassed bool
	select {
	case errB = <-doErrB:
		printProgressStr(markerH2ErrB, safeOneLine(errB))
	case <-sharedDest.secondAckResolved:
		poisonBypassed = true
		fmt.Println(markerH2PoisonBypassed)
	case <-time.After(15 * time.Second):
		fmt.Fprintf(os.Stderr, "%s: timed out waiting for worker B's shared-destination outcome\n", markerFatal)
		os.Exit(exitOpenOtherError)
	}

	codeB := "NONE"
	if errB != nil {
		if ce, ok := conduiterr.Get(errB); ok {
			codeB = ce.Code.Reason()
		}
	}
	printProgressStr(markerH2ErrBCode, codeB)

	if poisonBypassed {
		// Unreachable with the H2 fix intact (see the file doc comment and
		// nsource_h2_sigkill_test.go). Give the wrongly-issued ack a bounded
		// window to finish propagating through Conduit's own persist ->
		// plugin-ack -> upstream-commit chain (pkg/connector/source.go's own
		// invariant-1 ordering) before this process snapshots and blocks -
		// polling, not a blind sleep, exactly like
		// waitNSourceUpstreamCommitted (nsource_child.go).
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if c, cErr := builtB.upstream.Committed(); cErr == nil && c > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}

	// Tear down worker A, mirroring the production worker goroutine's own
	// unconditional Close call on every exit path (service.go's
	// runPipeline) - errors here are diagnostic only, not fatal: this
	// process is about to be SIGKILLed regardless, and Close is documented
	// as safe to call on an error path where Stop was never reached. Safe
	// here because worker A's Do() has ALREADY returned (errA, above) -
	// exactly the sequential Do-then-Close ordering production always uses.
	if err := workerA.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "close worker A: %v\n", err)
	}

	// Worker B is different: with the fix intact, doErrB already fired above
	// (poison-refused), so its Do() has ALSO already returned and Close() is
	// equally safe. In the unreachable-under-the-fix poisonBypassed branch,
	// though, worker B's Do() goroutine is (by this scenario's own design -
	// see cfg.totalB's doc) still blocked forever inside its now-exhausted
	// source's next Read() call - calling Close() concurrently with that
	// in-flight call is NOT the pattern production ever exercises (runPipeline
	// only ever calls Close() AFTER Do() has returned, sequentially, in the
	// SAME goroutine) and was confirmed, while developing this scenario's
	// non-vacuity verification, to panic inside connector.Source.Teardown's
	// internal WaitGroup ("reused before previous Wait has returned") - a
	// test-harness-only hazard of this deliberately-broken verification
	// configuration, not a finding about the engine. Skipped here rather
	// than papered over with extra synchronization this scenario doesn't
	// otherwise need.
	if !poisonBypassed {
		if err := workerB.Close(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "close worker B: %v\n", err)
		}
	}

	builtA.persister.WaitPendingWrites()
	if !poisonBypassed {
		builtB.persister.WaitPendingWrites()
	}

	fmt.Println(markerH2Confirmed)

	// Model a pipeline that has hit Degraded status and is waiting for an
	// operator to restart it - see the file doc comment for why an actual
	// process kill (not a graceful exit) is what closes out this run, and
	// why this is still deterministic (the parent waits for markerH2Confirmed
	// above, never a wall clock, before sending SIGKILL).
	select {}
}
