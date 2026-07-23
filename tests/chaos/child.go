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
	"strings"
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
	envChild        = "CONDUIT_CHAOS_CHILD"
	envDBDir        = "CONDUIT_CHAOS_DB_DIR"
	envUpstreamDir  = "CONDUIT_CHAOS_UPSTREAM_DIR"
	envPrune        = "CONDUIT_CHAOS_PRUNE"
	envPaceMS       = "CONDUIT_CHAOS_PACE_MS"
	envTotal        = "CONDUIT_CHAOS_TOTAL"
	instanceID      = "chaos-source"
	instancePlugin  = "chaos-plugin"
	instancePipe    = "chaos-pipeline"
	markerOpenGap   = "OPEN_GAP_ERROR"
	markerDone      = "DONE"
	markerFatal     = "FATAL"
	markerCorruptPo = "CORRUPT_POSITION"
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
	total       uint64
}

// parseChildEnv reads and validates the child's environment. Any failure
// here is a test-harness misconfiguration, not a scenario under test, so it
// exits immediately with exitBadArgs.
func parseChildEnv() childEnv {
	var cfg childEnv
	cfg.dbDir = os.Getenv(envDBDir)
	cfg.upstreamDir = os.Getenv(envUpstreamDir)
	cfg.prune = os.Getenv(envPrune) == "true"

	paceMS, err := strconv.Atoi(os.Getenv(envPaceMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envPaceMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMS = paceMS

	total, err := strconv.ParseUint(os.Getenv(envTotal), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envTotal, err)
		os.Exit(exitBadArgs)
	}
	cfg.total = total

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
// until the last acked position reaches total (or forever, if total is 0).
// It exits the process directly on any Read/Ack error.
func runReadAckLoop(ctx context.Context, src *connector.Source, total uint64) {
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

		if total > 0 {
			last, _ := decodePosition(positions[len(positions)-1])
			if last >= total {
				return
			}
		}
	}
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

	logger := log.New(zerolog.Nop()) // keep stdout clean; it is our progress-line channel

	db, err := badger.New(zerolog.Nop(), cfg.dbDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: open badger db: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	persister := connector.NewPersister(logger, db, connector.DefaultPersisterDelayThreshold, connector.DefaultPersisterBundleCountThreshold)
	store := connector.NewStore(db, logger)

	instance := loadOrCreateInstance(ctx, store)
	instance.Init(logger, persister)
	printResumePosition(instance)

	upstream, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	plugin := &chaosPlugin{store: upstream, total: cfg.total, paceMS: cfg.paceMS}

	fetcher := staticFetcher{instance.Plugin: staticDispenser{source: plugin}}
	c, err := instance.Connector(ctx, fetcher)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: build connector: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}
	src, ok := c.(*connector.Source)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unexpected connector type %T\n", markerFatal, c)
		os.Exit(exitBadArgs)
	}

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

	runReadAckLoop(ctx, src, cfg.total)

	// src.Ack only guarantees the ack was HANDED OFF to the plugin
	// (stream.Send rendezvous with the plugin's Recv) - it does not wait for
	// chaosPlugin.ackLoop's subsequent, synchronous upstream.Commit call to
	// finish. Without waiting here, this process could os.Exit before that
	// last commit's fsync completes, truncating the run by one position and
	// producing a false-positive gap that has nothing to do with the engine
	// code under test. This wait is test-harness synchronization only - it
	// has no analog in (and makes no claim about) production code.
	waitForUpstreamCommitted(upstream, cfg.total)

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
