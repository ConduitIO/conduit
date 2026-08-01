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
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/rs/zerolog"
)

// Environment variables the parent test process (harness.go) uses to
// configure a child run. This is a test-only, out-of-band protocol between
// this package's own test binary re-executing itself - it never touches
// conduit run's actual config surface (see doc.go / the design doc's
// observability section on this exact point).
const (
	envChild          = "CONDUIT_CHAOS_CHILD"
	envDBDir          = "CONDUIT_CHAOS_DB_DIR"
	envUpstreamDir    = "CONDUIT_CHAOS_UPSTREAM_DIR"
	envPrune          = "CONDUIT_CHAOS_PRUNE"
	envPaceMS         = "CONDUIT_CHAOS_PACE_MS"
	envPersistDelayMS = "CONDUIT_CHAOS_PERSIST_DELAY_MS"
	envTotal          = "CONDUIT_CHAOS_TOTAL"
	envSnapshotK      = "CONDUIT_CHAOS_SNAPSHOT_K"
	envSnapshotPaceMS = "CONDUIT_CHAOS_SNAPSHOT_PACE_MS"
	envNumKeys        = "CONDUIT_CHAOS_NUM_KEYS"
	envDriftAt        = "CONDUIT_CHAOS_DRIFT_AT"
	envSigtermMode    = "CONDUIT_CHAOS_SIGTERM_MODE"
	instanceID        = "chaos-source"
	instancePlugin    = "chaos-plugin"
	instancePipe      = "chaos-pipeline"
	markerOpenGap     = "OPEN_GAP_ERROR"
	markerDone        = "DONE"
	markerFatal       = "FATAL"
	markerCorruptPo   = "CORRUPT_POSITION"
	markerHandoff     = "HANDOFF"   // Property 1/2: producer crossed the snapshot->stream boundary, see upstream.go/produceLoop
	markerAckOrder    = "ACK_ORDER" // Property 3: per-key ack delivery ledger, see upstream.go/ackLoop
	// markerSigtermDone is the SIGTERM/invariant-7 case's completion marker
	// (sigterm_test.go, runChildSigterm) - deliberately distinct from
	// markerDone, which means "ran to cfg.total and tore down at the natural
	// end of the run". This marker means "a SIGTERM arrived mid-run and
	// Source.Teardown's flush-and-wait-then-stopStream ordering
	// (source.go:249-326) completed" - a graceful stop, not a completed run.
	markerSigtermDone = "SIGTERM_TEARDOWN_OK"

	// envValueTrue is the string childConfig.env() writes (via
	// strconv.FormatBool) for boolean env flags (envPrune, envSigtermMode) -
	// named so the comparisons in parseChildEnv/TestMain share one constant
	// instead of repeating the literal (goconst).
	envValueTrue = "true"
)

// exitCode values the parent harness distinguishes between. An OPEN_GAP_ERROR
// (chaosPlugin.Open refusing a stale resume) exits with exitOK: it is a
// clean, expected-shape outcome for the pruning-upstream scenario, and the
// printed marker line - not the exit code - is what carries the verdict.
const (
	exitOK             = 0
	exitOpenOtherError = 4
	exitCorruptState   = 3
	exitBadArgs        = 2
)

// isChildInvocation reports whether this process invocation should behave as
// a chaos child runner instead of running the package's actual Go tests -
// the standard "re-exec the test binary as a helper process" pattern (used
// by, e.g., the Go standard library's own os/exec tests).
func isChildInvocation() bool {
	return os.Getenv(envChild) == "1"
}

// childEnv holds the parsed configuration a parent test process passes to a
// chaos child via environment variables (see childConfig.env in harness.go).
type childEnv struct {
	dbDir       string
	upstreamDir string
	prune       bool
	paceMS      int
	// persistDelayMS overrides the persister debounce; 0 = default. See
	// childConfig.persistDelayMS in harness.go for why this is configurable.
	persistDelayMS int
	total          uint64

	// snapshotK/snapshotPaceMS: Property 1/2's two-phase producer knobs.
	// snapshotK == 0 means "no distinct snapshot phase" (DBZ-1's original
	// single-phase behavior) - see chaosPlugin's type doc (upstream.go).
	snapshotK      uint64
	snapshotPaceMS int

	// numKeys: Property 3's multi-key knob. numKeys <= 1 means "single,
	// unkeyed position space" (DBZ-1's original behavior) - see
	// chaosPlugin's type doc (upstream.go).
	numKeys int

	// driftAt: Property 4's synthetic drift/poison-marker knob. 0 (the
	// default) means no record in this run carries the marker - see
	// chaosPlugin.driftAt's field doc (upstream.go).
	driftAt uint64

	// sigtermMode: if true, runChildSigterm runs instead of runChild - an
	// unbounded read/ack loop that waits for SIGTERM and drives
	// Source.Teardown's flush-and-wait-then-stopStream ordering
	// (source.go:249-326) explicitly, rather than tearing down at the
	// natural end of a total-bounded run. See sigterm_test.go.
	sigtermMode bool
}

// parseChildEnv reads and validates the child's environment. Any failure
// here is a test-harness misconfiguration, not a scenario under test, so it
// exits immediately with exitBadArgs.
func parseChildEnv() childEnv {
	var cfg childEnv
	cfg.dbDir = os.Getenv(envDBDir)
	cfg.upstreamDir = os.Getenv(envUpstreamDir)
	cfg.prune = os.Getenv(envPrune) == envValueTrue

	paceMS, err := strconv.Atoi(os.Getenv(envPaceMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envPaceMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMS = paceMS

	// Optional: 0/unset means use connector.DefaultPersisterDelayThreshold.
	if v := os.Getenv(envPersistDelayMS); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envPersistDelayMS, err)
			os.Exit(exitBadArgs)
		}
		cfg.persistDelayMS = d
	}

	total, err := strconv.ParseUint(os.Getenv(envTotal), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envTotal, err)
		os.Exit(exitBadArgs)
	}
	cfg.total = total

	// snapshotK/snapshotPaceMS/numKeys default to 0 (via strconv's error
	// return on an empty string) when the parent didn't opt into Property
	// 1/2's two-phase producer or Property 3's multi-key mode - childConfig.env
	// always sets these explicitly (defaulting to "0"), so an empty/unparseable
	// value here IS a harness misconfiguration, not a legitimate "unset".
	snapshotK, err := strconv.ParseUint(os.Getenv(envSnapshotK), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envSnapshotK, err)
		os.Exit(exitBadArgs)
	}
	cfg.snapshotK = snapshotK

	snapshotPaceMS, err := strconv.Atoi(os.Getenv(envSnapshotPaceMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envSnapshotPaceMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.snapshotPaceMS = snapshotPaceMS

	numKeys, err := strconv.Atoi(os.Getenv(envNumKeys))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envNumKeys, err)
		os.Exit(exitBadArgs)
	}
	cfg.numKeys = numKeys

	driftAt, err := strconv.ParseUint(os.Getenv(envDriftAt), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envDriftAt, err)
		os.Exit(exitBadArgs)
	}
	cfg.driftAt = driftAt

	cfg.sigtermMode = os.Getenv(envSigtermMode) == envValueTrue

	if cfg.dbDir == "" || cfg.upstreamDir == "" {
		fmt.Fprintf(os.Stderr, "%s: %s and %s are required\n", markerFatal, envDBDir, envUpstreamDir)
		os.Exit(exitBadArgs)
	}
	return cfg
}

// loadOrCreateInstance loads the connector.Instance persisted by a previous
// (possibly SIGKILL'd) run of this same chaos-source ID, or creates a fresh
// one if none exists yet. It exits the process directly (exitCorruptState)
// if the persisted state exists but fails to deserialize — per invariant 2,
// that is itself a failing outcome for this test, never silently treated as
// "fresh start".
func loadOrCreateInstance(ctx context.Context, store *connector.Store) *connector.Instance {
	instance, err := store.Get(ctx, instanceID)
	switch {
	case err == nil:
		return instance // resumed from a previous (possibly killed) run's persisted state
	case cerrors.Is(err, database.ErrKeyNotExist):
		return &connector.Instance{
			ID:            instanceID,
			Type:          connector.TypeSource,
			Config:        connector.Config{Name: instanceID, Settings: map[string]string{}},
			PipelineID:    instancePipe,
			Plugin:        instancePlugin,
			ProvisionedBy: connector.ProvisionTypeAPI,
		}
	default:
		fmt.Printf("%s: %v\n", markerCorruptPo, err)
		os.Exit(exitCorruptState)
		return nil // unreachable
	}
}

// printResumePosition emits the diagnostic line the parent test relies on to
// learn exactly where Conduit persisted state says this run is about to
// resume from — printed unconditionally (fresh start included, where it's
// the empty position) so the parent can compare it against the upstream's
// independently-read committed watermark without opening the badger DB
// itself.
func printResumePosition(instance *connector.Instance) {
	if src, ok := instance.State.(connector.SourceState); ok {
		fmt.Printf("RESUME_POSITION %s\n", src.Position)
		return
	}
	fmt.Printf("RESUME_POSITION %s\n", opencdc.Position(nil))
}

// runReadAckLoop reads and immediately acks records one batch at a time
// until termination, then returns. It exits the process directly on any
// Read/Ack error.
//
// Termination is decided one of two ways, selected by keyed:
//   - keyed == false (Property 1/2, single global position space): decode
//     the last acked position and stop once it reaches total. This is what
//     makes RESTART termination correct - a resumed run only reads/acks the
//     REMAINING positions (resume+1..total), so a plain records-processed
//     count would stop too early.
//   - keyed == true (Property 3, multi-key "k<i>:<seq>" positions): stop
//     once the total NUMBER of records processed in this run reaches total.
//     Property 3 never restarts (see chaosPlugin's type doc, upstream.go),
//     so there is no resume offset to account for, and the keyed position
//     encoding isn't a single monotone counter decodePosition could compare
//     against total in the first place.
func runReadAckLoop(ctx context.Context, src *connector.Source, total uint64, keyed bool) {
	var processed uint64
	for {
		recs, err := src.Read(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: read failed: %v\n", markerFatal, err)
			os.Exit(exitOpenOtherError)
		}

		positions := make([]opencdc.Position, len(recs))
		for i, r := range recs {
			positions[i] = r.Position
		}
		if err := src.Ack(ctx, positions); err != nil {
			fmt.Fprintf(os.Stderr, "%s: ack failed: %v\n", markerFatal, err)
			os.Exit(exitOpenOtherError)
		}

		if total == 0 {
			continue
		}
		if keyed {
			processed += uint64(len(recs))
			if processed >= total {
				return
			}
			continue
		}
		last, _ := decodePosition(positions[len(positions)-1])
		if last >= total {
			return
		}
	}
}

// childBuilt bundles the real engine pieces buildChild wires together for a
// single child run: a real *connector.Source, backed by a real on-disk
// badger DB and connector.Persister, driven by an in-process chaosPlugin.
// Factored out of runChild so Property 4's in-process funnel integration
// (property4_test.go) can build and drive the IDENTICAL construction through
// a real funnel.Worker instead of runReadAckLoop's direct src.Read/src.Ack
// calls - see doc.go's note on why the direct loop bypasses the
// funnel/DLQ/destination entirely, which is exactly the gap Property 4
// closes.
type childBuilt struct {
	logger    log.CtxLogger
	persister *connector.Persister
	store     *connector.Store
	instance  *connector.Instance
	upstream  *upstreamStore
	plugin    *chaosPlugin
	src       *connector.Source
}

// buildChild opens (or creates) the badger DB at cfg.dbDir and the
// upstreamStore at cfg.upstreamDir, then constructs a real *connector.Source
// around an in-process chaosPlugin configured per cfg - the exact
// construction runChild used inline before this refactor. Every error here is
// a test-harness misconfiguration (bad dirs, unexpected connector type), not
// a scenario under test, mirroring the exitBadArgs treatment the inline code
// gave each of these failures.
func buildChild(ctx context.Context, cfg childEnv) (*childBuilt, error) {
	logger := log.New(zerolog.Nop()) // keep stdout clean; it is our progress-line channel

	db, err := badger.New(zerolog.Nop(), cfg.dbDir)
	if err != nil {
		return nil, fmt.Errorf("open badger db: %w", err)
	}

	persistDelay := connector.DefaultPersisterDelayThreshold
	if cfg.persistDelayMS > 0 {
		persistDelay = time.Duration(cfg.persistDelayMS) * time.Millisecond
	}
	persister := connector.NewPersister(logger, db, persistDelay, connector.DefaultPersisterBundleCountThreshold)
	store := connector.NewStore(db, logger)

	instance := loadOrCreateInstance(ctx, store)
	instance.Init(logger, persister)

	upstream, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	if err != nil {
		return nil, err
	}
	plugin := &chaosPlugin{
		store:          upstream,
		total:          cfg.total,
		paceMS:         cfg.paceMS,
		snapshotK:      cfg.snapshotK,
		snapshotPaceMS: cfg.snapshotPaceMS,
		numKeys:        cfg.numKeys,
		driftAt:        cfg.driftAt,
	}

	fetcher := staticFetcher{instance.Plugin: staticDispenser{source: plugin}}
	c, err := instance.Connector(ctx, fetcher)
	if err != nil {
		return nil, fmt.Errorf("build connector: %w", err)
	}
	src, ok := c.(*connector.Source)
	if !ok {
		return nil, fmt.Errorf("unexpected connector type %T", c)
	}

	return &childBuilt{
		logger:    logger,
		persister: persister,
		store:     store,
		instance:  instance,
		upstream:  upstream,
		plugin:    plugin,
		src:       src,
	}, nil
}

// runChild is the entire child-process program: build a real
// pkg/connector.Source, backed by a real on-disk badger DB (the same
// production persister backend, via connector.NewPersister) and the
// in-process chaosPlugin, then read+ack records sequentially until `total`
// is reached. It never returns - it always calls os.Exit.
//
// This process is expected to be killed with SIGKILL by the parent test at
// an arbitrary point; no cleanup on that path is possible or intended - that
// is the entire point of the test (see doc.go).
func runChild() {
	ctx := context.Background()
	cfg := parseChildEnv()

	built, err := buildChild(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	printResumePosition(built.instance)

	src := built.src
	persister := built.persister
	upstream := built.upstream
	plugin := built.plugin

	if err := src.Open(ctx); err != nil {
		if containsGapMarker(err) {
			// This IS the scenario this test exists to surface (see doc.go):
			// on a pruning upstream, Conduit's persisted position lagged
			// behind what was already durably committed at the moment of the
			// kill, and the upstream can no longer supply the gap. Print the
			// marker so the parent test can assert on it; this is an
			// expected, clean exit for that scenario, not a
			// test-infrastructure failure.
			fmt.Printf("%s: %v\n", markerOpenGap, err)
			os.Exit(exitOK)
		}
		fmt.Fprintf(os.Stderr, "%s: source open failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	keyed := cfg.numKeys > 1
	runReadAckLoop(ctx, src, cfg.total, keyed)

	// src.Ack only guarantees the ack was HANDED OFF to the plugin
	// (stream.Send rendezvous with the plugin's Recv) - it does not wait for
	// chaosPlugin.ackLoop's subsequent side effects to finish. Without
	// waiting here, this process could os.Exit before those side effects
	// land, truncating the run by one position and producing a
	// false-positive gap/missing-ledger-entry that has nothing to do with
	// the engine code under test. This wait is test-harness synchronization
	// only - it has no analog in (and makes no claim about) production code.
	//
	// Property 3 (keyed) has no single upstream watermark to wait on (see
	// ackLoop) - it waits on chaosPlugin.ackedCount instead, which tracks
	// exactly the same thing (every ACK_ORDER line has actually been
	// printed) via a different signal.
	if keyed {
		if !plugin.waitForAckedCount(cfg.total, 5*time.Second) {
			fmt.Fprintf(os.Stderr, "%s: timed out waiting for plugin to ack %d positions\n", markerFatal, cfg.total)
			os.Exit(exitOpenOtherError)
		}
	} else {
		waitForUpstreamCommitted(upstream, cfg.total)
	}

	// Graceful path: only reached when this run was allowed to finish
	// (the parent didn't SIGKILL it). Tear down cleanly so the final
	// persisted position is flushed and observable by the parent's
	// post-run assertions.
	if err := src.Teardown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: teardown failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	persister.WaitPendingWrites()

	fmt.Println(markerDone)
	os.Exit(exitOK)
}

// runChildSigterm is runChild's counterpart for the SIGTERM/invariant-7 case
// (sigterm_test.go). Unlike runChild, it does not run to cfg.total and tear
// down at the natural end of the run - it runs the read/ack loop
// indefinitely in the background and waits for a SIGTERM, then drives
// Source.Teardown's flush-and-wait-then-stopStream ordering
// (source.go:249-326) explicitly: invariant 7 requires that the ack deferred
// at the moment the signal lands is actually SENT to the plugin (and, per
// Ack's own invariant 1, only sent once the resulting position is durably
// flushed) before the stream is torn down - the graceful-path complement to
// sigkill_test.go's/property2_test.go's SIGKILL cases, which instead prove
// "eventually safe after a restart".
//
// Prints markerSigtermDone (never markerDone, which means "reached cfg.total
// and tore down normally") once Teardown has confirmed the deferred ack was
// drained, so the parent test can tell this exit apart from both a normal
// completed run and a crash.
func runChildSigterm() {
	ctx := context.Background()
	cfg := parseChildEnv()

	built, err := buildChild(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	printResumePosition(built.instance)

	src := built.src
	if err := src.Open(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: source open failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	// stopping + processingMu mirror funnel.Worker.Stop's own
	// stop-flag-plus-processingLock discipline (worker.go's Stop and doTask):
	// Source.Teardown's own doc comment explicitly relies on "no new Ack call
	// can start once a Teardown has begun" being guaranteed by the CALLER,
	// not by Teardown itself. In production that guarantee comes from
	// funnel.Worker's processingLock; this child has no funnel.Worker (see
	// property4_test.go's package doc for why Property 4 is the one that
	// needs a real Worker - this SIGTERM case does not), so it must
	// reconstruct the same discipline directly: processingMu is held for
	// exactly one Read+Ack cycle at a time, and the signal handler
	// synchronizes on it before calling Teardown, guaranteeing no cycle is
	// in flight (and none will start) once Teardown begins. Without this,
	// a late Ack racing Teardown's own forced flush could register a
	// position that never gets flushed before the process exits - not a
	// bug in Source.Teardown, but exactly the caller-side race its doc
	// comment warns about.
	var stopping atomic.Bool
	var processingMu sync.Mutex
	readLoopDone := make(chan struct{})
	go func() {
		defer close(readLoopDone)
		for {
			processingMu.Lock()
			if stopping.Load() {
				processingMu.Unlock()
				return
			}

			recs, err := src.Read(ctx)
			if err != nil {
				processingMu.Unlock()
				if stopping.Load() {
					return // expected: Teardown's stopStream unblocked this Read
				}
				fmt.Fprintf(os.Stderr, "%s: read failed: %v\n", markerFatal, err)
				os.Exit(exitOpenOtherError)
			}

			positions := make([]opencdc.Position, len(recs))
			for i, r := range recs {
				positions[i] = r.Position
			}
			ackErr := src.Ack(ctx, positions)
			processingMu.Unlock()
			if ackErr != nil {
				if stopping.Load() {
					return // expected: the stream closed while Teardown was tearing it down
				}
				fmt.Fprintf(os.Stderr, "%s: ack failed: %v\n", markerFatal, ackErr)
				os.Exit(exitOpenOtherError)
			}
		}
	}()

	<-sigCh
	stopping.Store(true)
	// Block until no Read+Ack cycle is in flight (see the goroutine's own
	// comment above) - after this returns, the reader goroutine is
	// guaranteed to observe stopping==true and exit without starting
	// another cycle, so no Ack can race what follows.
	processingMu.Lock()
	processingMu.Unlock() //nolint:staticcheck // deliberate lock/unlock handshake, not a no-op - see comment above

	// Invariant 7, the property this entire case exists to pin: Teardown
	// forces the pending flush and waits (bounded) for its resulting
	// deferred ack to actually be SENT to the plugin - which, per Ack's own
	// invariant-1 ordering, only happens once that flush's write is durably
	// confirmed - BEFORE stopping the stream (source.go:249-326). Only once
	// this returns is it safe to treat whatever was in flight at signal-time
	// as drained, not dropped.
	if err := src.Teardown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: teardown failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	<-readLoopDone

	built.persister.WaitPendingWrites() // belt-and-suspenders; Teardown's own wait already covers this (see its doc comment)

	// Teardown's (and WaitPendingWrites') guarantee only reaches as far as
	// "the deferred ack was HANDED OFF to the plugin" (stream.Send
	// rendezvous with chaosPlugin.ackLoop's Recv) - exactly like
	// runReadAckLoop's own tail comment explains, it does not wait for
	// ackLoop's subsequent side effect (upstreamStore.Commit) to finish.
	// Without this wait, this process could exit while that commit is still
	// in flight, making the parent test observe a stale upstream watermark
	// that has nothing to do with whether Teardown actually drained the
	// deferred ack - see waitForUpstreamAtLeast.
	finalPos := readPersistedPosition(ctx, built.store)
	if !waitForUpstreamAtLeast(built.upstream, finalPos, 5*time.Second) {
		fmt.Fprintf(os.Stderr, "%s: timed out waiting for upstream to catch up to persisted position %d\n", markerFatal, finalPos)
		os.Exit(exitOpenOtherError)
	}

	fmt.Println(markerSigtermDone)
	os.Exit(exitOK)
}

// readPersistedPosition reads Conduit's own durably-persisted resume
// position directly from the store (a fresh round-trip through
// connector.Store.Get/decode, not the in-memory Instance this process
// already holds), mirroring printResumePosition's own durability check.
// Returns 0 if nothing has been acked/persisted yet.
func readPersistedPosition(ctx context.Context, store *connector.Store) uint64 {
	instance, err := store.Get(ctx, instanceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: read persisted position: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	state, ok := instance.State.(connector.SourceState)
	if !ok {
		return 0
	}
	pos, err := decodePosition(state.Position)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: decode persisted position: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	return pos
}

// waitForUpstreamAtLeast blocks (polling) until the upstream store reports at
// least target positions committed, or returns false once timeout has
// elapsed. See runChildSigterm's call site for why this - not just
// Persister.WaitPendingWrites - is needed to bridge the "handed off to the
// plugin" vs. "the plugin's side effect actually landed" gap.
func waitForUpstreamAtLeast(upstream *upstreamStore, target uint64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		committed, err := upstream.Committed()
		if err == nil && committed >= target {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	committed, err := upstream.Committed()
	return err == nil && committed >= target
}

// waitForUpstreamCommitted blocks briefly until the upstream store reports
// total positions committed, so this process doesn't exit while
// chaosPlugin.ackLoop's goroutine still has an in-flight, not-yet-durable
// commit for the very last ack this run just sent. See its call site for
// why this is needed. A no-op when total is 0 (unbounded runs never reach
// this graceful-exit path in the first place).
func waitForUpstreamCommitted(upstream *upstreamStore, total uint64) {
	if total == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		committed, err := upstream.Committed()
		if err == nil && committed >= total {
			return
		}
		time.Sleep(time.Millisecond)
	}
	// Falling through here would let the caller proceed to Teardown/DONE
	// while the plugin's last commit is still in flight, silently
	// undermining the very "committed == total" guarantee the parent test's
	// duplicate-not-gap assertion depends on - so this must exit, not warn
	// and continue.
	fmt.Fprintf(os.Stderr, "%s: timed out waiting for upstream to reach position %d\n", markerFatal, total)
	os.Exit(exitOpenOtherError)
}

// containsGapMarker reports whether err is (or wraps) the specific,
// structural gap chaosPlugin.Open (upstream.go) returns when a pruning
// upstream can no longer supply data behind its already-committed watermark.
// pkg/connector.Source.open wraps that error ("could not open source
// connector plugin: %w"), so a substring check - not a prefix check - is
// needed. A substring check on a plain fmt.Errorf (rather than a typed
// sentinel) is enough here: this is test-only harness code checking
// test-only harness output, not a production error contract.
func containsGapMarker(err error) bool {
	return err != nil && strings.Contains(err.Error(), "GAP:")
}

// staticDispenser and staticFetcher wire a single, already-constructed
// chaosPlugin into pkg/connector's plugin-dispensing machinery
// (connector.PluginDispenserFetcher -> connectorPlugin.Dispenser ->
// connectorPlugin.SourcePlugin), mirroring the same pattern
// pkg/connector's own tests use (fakePluginFetcher in instance_test.go).
type staticDispenser struct {
	source connectorPlugin.SourcePlugin
}

func (d staticDispenser) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("staticDispenser: DispenseSpecifier not implemented")
}

func (d staticDispenser) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	return d.source, nil
}

func (d staticDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return nil, cerrors.New("staticDispenser: DispenseDestination not implemented")
}

type staticFetcher map[string]connectorPlugin.Dispenser

func (f staticFetcher) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	d, ok := f[name]
	if !ok {
		return nil, cerrors.Errorf("staticFetcher: no dispenser registered for plugin %q", name)
	}
	return d, nil
}
