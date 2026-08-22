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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	lifecycle "github.com/conduitio/conduit/pkg/lifecycle-poc"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/rs/zerolog"
)

// This file is PR-3 of the arch-v2 recovery epic (recovery_test.go's package
// doc explains the two crash scenarios it drives). Unlike child.go/upstream.go
// (which exercise pkg/connector.Source directly - the connector/persister
// durability boundary already proved gap-free by sigkill_test.go), this file
// builds a REAL pkg/lifecycle-poc.Service around a real funnel.Worker
// (source -> destination), so a SIGKILL against this child process exercises
// the recovery loop itself (Service.recoverPipeline / StartWithBackoff /
// runPipeline's recovery arm, pkg/lifecycle-poc/service.go) - composed on top
// of, not instead of, that already-proven connector/persister boundary.

// lcPersistDelay is this file's persister debounce - fast, unlike the
// connector-layer sigkill tests, which use the production-matching
// connector.DefaultPersisterDelayThreshold to time a kill relative to that
// specific window (see buildLifecycleChild's own comment on NewPersister for
// why this scenario's crash windows don't need that). Hoisted to a constant,
// used at both buildLifecycleChild's connector.NewPersister call and
// runChildLifecycle's waitForUpstreamCommitted call, so the two cannot drift
// - they did, once: the drain wait passed
// connector.DefaultPersisterDelayThreshold (1s) under a comment claiming
// "this child never overrides the debounce", while NewPersister was in fact
// passing this file's own 10ms override two calls above. Benign in
// direction (the derived stall budget was too generous, not too tight), but
// exactly the kind of quiet divergence between a drain budget and the
// persister that actually gates it this constant exists to make impossible.
const lcPersistDelay = 10 * time.Millisecond

// Environment variables for this file's child mode - a separate, small
// protocol from child.go's envXxx constants (parsed by parseLCChildEnv, not
// parseChildEnv), reusing only the generic re-exec/spawn/SIGKILL machinery in
// harness.go (spawnChildWithEnv, childProcess). Kept separate rather than
// added as new childConfig/childEnv fields because this scenario's shape
// (durable source position AND a durable destination delivery ledger AND a
// pipeline-status marker stream) doesn't fit childConfig's existing
// single-connector shape without distorting it.
const (
	envLCMode       = "CONDUIT_CHAOS_LC_MODE"
	envLCDBDir      = "CONDUIT_CHAOS_LC_DB_DIR"
	envLCUpstream   = "CONDUIT_CHAOS_LC_UPSTREAM_DIR"
	envLCMainDir    = "CONDUIT_CHAOS_LC_MAIN_DIR"
	envLCDLQDir     = "CONDUIT_CHAOS_LC_DLQ_DIR"
	envLCTotal      = "CONDUIT_CHAOS_LC_TOTAL"
	envLCPaceMS     = "CONDUIT_CHAOS_LC_PACE_MS"
	envLCFailAt     = "CONDUIT_CHAOS_LC_FAIL_AT"
	envLCMinDelayMS = "CONDUIT_CHAOS_LC_MIN_DELAY_MS"
	envLCMaxDelayMS = "CONDUIT_CHAOS_LC_MAX_DELAY_MS"
	envLCGraceful   = "CONDUIT_CHAOS_LC_GRACEFUL"

	lcSourceInstanceID = "lc-source"
	lcDestInstanceID   = "lc-dest"
	lcDLQInstanceID    = "lc-dlq"
	lcSourcePlugin     = "lc-source-plugin"
	lcDestPlugin       = "lc-dest-plugin"
	lcDLQPlugin        = "lc-dlq-plugin"
	lcPipelineID       = "lc-pipeline"

	// markerLCStatus lines ("LC_STATUS <name>") are this child's only channel
	// for telling the parent test process - a separate OS process, unable to
	// see this process's memory, which is the entire reason this scenario is
	// cross-process rather than in-process (see recovery_test.go's package
	// doc) - which pipeline.Status this pipeline just reached. Printed by
	// lcPipelineService.UpdateStatus, exactly mirroring how printProgress's
	// "READ"/"ACK" lines already let the parent observe chaosPlugin's
	// progress without opening the badger DB itself.
	markerLCStatus = "LC_STATUS"
	// markerLCDone is printed once the graceful "run to completion" child
	// variant (runChildLifecycle's cfg.graceful branch) has stopped the
	// pipeline cleanly and confirmed every record through cfg.total was
	// acked - the counterpart to child.go's markerDone for this scenario.
	markerLCDone = "LC_DONE"
)

// lcChildConfig is the parent-side (harness) counterpart to lcChildEnv,
// mirroring childConfig's env-building role in harness.go.
type lcChildConfig struct {
	dbDir       string
	upstreamDir string
	mainDir     string
	dlqDir      string

	total  uint64
	paceMS int
	failAt uint64 // 0 = this dispense never fails

	minDelayMS int
	maxDelayMS int

	// graceful selects the child's two variants (see runChildLifecycle):
	// false is the crashable variant used by both of recovery_test.go's
	// SIGKILL scenarios (start, then hang forever letting the parent kill it
	// at a marker-gated moment); true is the "run to completion and stop
	// gracefully" variant used for the post-crash restart that proves the
	// pipeline finishes end-to-end despite the earlier kill.
	graceful bool
}

func (c lcChildConfig) env() []string {
	return []string{
		envChild + "=1", // isChildInvocation's gate, shared with child.go/child.SigtermMode
		envLCMode + "=" + envValueTrue,
		envLCDBDir + "=" + c.dbDir,
		envLCUpstream + "=" + c.upstreamDir,
		envLCMainDir + "=" + c.mainDir,
		envLCDLQDir + "=" + c.dlqDir,
		envLCTotal + "=" + strconv.FormatUint(c.total, 10),
		envLCPaceMS + "=" + strconv.Itoa(c.paceMS),
		envLCFailAt + "=" + strconv.FormatUint(c.failAt, 10),
		envLCMinDelayMS + "=" + strconv.Itoa(c.minDelayMS),
		envLCMaxDelayMS + "=" + strconv.Itoa(c.maxDelayMS),
		envLCGraceful + "=" + strconv.FormatBool(c.graceful),
	}
}

// lcChildEnv is the child-side parsed form of lcChildConfig, read back out of
// the environment by parseLCChildEnv - mirroring child.go's childEnv/
// parseChildEnv split.
type lcChildEnv struct {
	dbDir       string
	upstreamDir string
	mainDir     string
	dlqDir      string

	total  uint64
	paceMS int
	failAt uint64

	minDelayMS int
	maxDelayMS int

	graceful bool
}

// parseLCChildEnv reads and validates this child's environment. Like
// parseChildEnv, any failure here is a test-harness misconfiguration, not a
// scenario under test, so it exits immediately with exitBadArgs.
func parseLCChildEnv() lcChildEnv {
	var cfg lcChildEnv
	cfg.dbDir = os.Getenv(envLCDBDir)
	cfg.upstreamDir = os.Getenv(envLCUpstream)
	cfg.mainDir = os.Getenv(envLCMainDir)
	cfg.dlqDir = os.Getenv(envLCDLQDir)

	total, err := strconv.ParseUint(os.Getenv(envLCTotal), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envLCTotal, err)
		os.Exit(exitBadArgs)
	}
	cfg.total = total

	paceMS, err := strconv.Atoi(os.Getenv(envLCPaceMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envLCPaceMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.paceMS = paceMS

	failAt, err := strconv.ParseUint(os.Getenv(envLCFailAt), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envLCFailAt, err)
		os.Exit(exitBadArgs)
	}
	cfg.failAt = failAt

	minDelayMS, err := strconv.Atoi(os.Getenv(envLCMinDelayMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envLCMinDelayMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.minDelayMS = minDelayMS

	maxDelayMS, err := strconv.Atoi(os.Getenv(envLCMaxDelayMS))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid %s: %v\n", markerFatal, envLCMaxDelayMS, err)
		os.Exit(exitBadArgs)
	}
	cfg.maxDelayMS = maxDelayMS

	cfg.graceful = os.Getenv(envLCGraceful) == envValueTrue

	if cfg.dbDir == "" || cfg.upstreamDir == "" || cfg.mainDir == "" || cfg.dlqDir == "" {
		fmt.Fprintf(os.Stderr, "%s: %s, %s, %s and %s are required\n", markerFatal, envLCDBDir, envLCUpstream, envLCMainDir, envLCDLQDir)
		os.Exit(exitBadArgs)
	}
	return cfg
}

// deliveryLog durably records every (sourceID, position) pair a destination
// plugin has written, so a post-crash assertion can verify - from disk,
// independent of any in-memory state a SIGKILL destroys - exactly what
// actually reached the destination before the kill (see
// recoveryDestinationPlugin.loop, and recovery_test.go's invariant-1
// assertion). Modeled on upstreamStore's own durability approach
// (openUpstreamStore/Commit), but as a set of files (one per delivered
// (sourceID, position) pair) rather than a single watermark, since the
// destination is not guaranteed to receive positions strictly in increasing
// order across a restart-induced duplicate delivery.
//
// sourceID keys every entry alongside position (see Record/deliveryKey)
// because positions are connector-defined and unique only WITHIN a source
// (nsource_child.go's doc): once N sources share a destination and its one
// deliveryLog, two sources can legitimately deliver byte-identical position
// values, and keying on position alone would let one source's delivery
// silently stand in for the other's (see nsource_sigkill_test.go, whose
// entire point is proving that does NOT happen). Every caller before that
// scenario (fanout_sigkill_test.go, recovery_test.go) always passes sourceID
// == "" - a single, un-shared destination per log has nothing to
// disambiguate - and deliveryKey's "" fast path keeps their on-disk file
// names byte-for-byte identical to this type's original, pre-sourceID shape.
type deliveryLog struct {
	dir string
}

func openDeliveryLog(dir string) (*deliveryLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("deliveryLog: mkdir %s: %w", dir, err)
	}
	return &deliveryLog{dir: dir}, nil
}

// deliveryKey returns the on-disk file name for (sourceID, pos), and
// parseDeliveryKey is its exact inverse. sourceID == "" (every caller before
// the N-source collision scenario) maps to the plain position string,
// unchanged from this type's original naming - so every existing caller's
// files are completely unaffected by sourceID's addition. A non-empty
// sourceID is joined with "-": position digits never contain "-", so
// splitting on the LAST "-" (see parseDeliveryKey) recovers sourceID exactly
// even if sourceID itself contains one (e.g. "nsource-a").
func deliveryKey(sourceID string, pos uint64) string {
	if sourceID == "" {
		return strconv.FormatUint(pos, 10)
	}
	return sourceID + "-" + strconv.FormatUint(pos, 10)
}

// parseDeliveryKey is deliveryKey's inverse: it recovers (sourceID, pos)
// from an on-disk file name, or reports ok == false for anything that isn't
// a name deliveryKey could have produced (e.g. a stray non-delivery file).
// A name with no "-" is treated as the sourceID == "" case (a plain position
// string); otherwise the LAST "-" splits sourceID from the trailing position
// digits - see deliveryKey's doc for why the last, not first, split is safe.
func parseDeliveryKey(name string) (sourceID string, pos uint64, ok bool) {
	if n, err := strconv.ParseUint(name, 10, 64); err == nil {
		return "", n, true
	}
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(name[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return name[:i], n, true
}

// Record durably marks (sourceID, pos) as delivered: an empty file, created
// and fsynced before returning, named per deliveryKey. The file's mere
// existence is the entire signal - unlike upstreamStore's single rewritten
// watermark file, there is nothing to tear here: a crash before this returns
// simply means the file doesn't exist yet (equivalent to "not yet
// delivered"), and a duplicate delivery (expected under at-least-once) is a
// harmless re-create of a file that's already there.
func (l *deliveryLog) Record(sourceID string, pos uint64) error {
	name := filepath.Join(l.dir, deliveryKey(sourceID, pos))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("deliveryLog: create %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("deliveryLog: fsync %s: %w", name, err)
	}
	return f.Close()
}

// Positions returns every durably delivered position, read fresh from disk
// (so a freshly restarted/independent process observes exactly what a
// previous, possibly killed, process actually delivered), sorted ascending -
// WITHOUT source attribution. Every caller of this method predates the
// N-source collision scenario and always records with sourceID == "" (a
// single, un-shared destination has nothing to attribute), so this remains
// exactly the plain numeric listing it always was. A scenario that shares one
// deliveryLog across sources with colliding positions must use
// PositionsBySource instead - conflating two sources' entries into one
// []uint64 here would silently hide exactly the misattribution bug that
// scenario exists to catch.
func (l *deliveryLog) Positions() ([]uint64, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("deliveryLog: readdir %s: %w", l.dir, err)
	}
	out := make([]uint64, 0, len(entries))
	for _, e := range entries {
		_, pos, ok := parseDeliveryKey(e.Name())
		if !ok {
			continue // ignore any stray non-delivery file
		}
		out = append(out, pos)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DeliveryRecord attributes one durably-recorded delivery to the source that
// produced it - see Record's (sourceID, pos) keying and PositionsBySource.
type DeliveryRecord struct {
	SourceID string
	Position uint64
}

// PositionsBySource returns every durably delivered (sourceID, position)
// pair, read fresh from disk (deliveryKey's inverse, via parseDeliveryKey),
// unsorted. Unlike Positions, this distinguishes two sources' colliding
// position values instead of conflating them into one []uint64 - the exact
// case the N-source shared-destination collision scenario
// (nsource_sigkill_test.go) exists to prove is handled correctly end-to-end.
func (l *deliveryLog) PositionsBySource() ([]DeliveryRecord, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("deliveryLog: readdir %s: %w", l.dir, err)
	}
	out := make([]DeliveryRecord, 0, len(entries))
	for _, e := range entries {
		sourceID, pos, ok := parseDeliveryKey(e.Name())
		if !ok {
			continue // ignore any stray non-delivery file
		}
		out = append(out, DeliveryRecord{SourceID: sourceID, Position: pos})
	}
	return out, nil
}

// recoverySourcePlugin is this scenario's in-process pconnector.SourcePlugin:
// a single-key, resumable producer (like chaosPlugin's single-phase mode),
// plus the one capability chaosPlugin deliberately doesn't have - injecting a
// transient (non-fatal) stream error partway through, to drive
// pkg/lifecycle-poc.Service into its recovery loop. Kept as its own type
// (rather than extending chaosPlugin/upstream.go) to keep this PR's diff
// isolated from Properties 1-4's already-reviewed, heavily cross-referenced
// producer.
type recoverySourcePlugin struct {
	store  *upstreamStore
	total  uint64 // 0 means unbounded
	paceMS int
	failAt uint64 // 0 = never fail; otherwise inject a transient error just before producing this position

	mu         sync.Mutex
	nextToRead uint64
}

var _ pconnector.SourcePlugin = (*recoverySourcePlugin)(nil)

func (p *recoverySourcePlugin) Configure(context.Context, pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	return pconnector.SourceConfigureResponse{}, nil
}

func (p *recoverySourcePlugin) Open(_ context.Context, req pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	resume, err := decodePosition(req.Position)
	if err != nil {
		return pconnector.SourceOpenResponse{}, fmt.Errorf("lc source plugin: invalid resume position %q: %w", req.Position, err)
	}
	p.mu.Lock()
	p.nextToRead = resume
	p.mu.Unlock()
	return pconnector.SourceOpenResponse{}, nil
}

func (p *recoverySourcePlugin) Run(ctx context.Context, stream pconnector.SourceRunStream) error {
	inmemStream, ok := stream.(*builtin.InMemorySourceRunStream)
	if !ok {
		return fmt.Errorf("lc source plugin: unexpected stream type %T", stream)
	}
	inmemStream.Init(ctx)
	server := inmemStream.Server()

	p.mu.Lock()
	start := p.nextToRead
	p.mu.Unlock()

	go p.produceLoop(inmemStream, server, start)
	go p.ackLoop(server)
	return nil
}

// produceLoop delivers records start+1, start+2, ... up to p.total
// (inclusive), exactly like chaosPlugin.produceLoop's single-phase mode. If
// failAt is nonzero, the instant production would reach that position it
// instead closes the stream with a synthetic transient error - surfacing as a
// plain (non-fatal) error from pkg/connector.Source.Read (via
// inmemStream.Client().Recv() returning the close reason - see stream.go),
// which is exactly the shape of error that drives pkg/lifecycle-poc's
// runPipeline into its recovery arm (the `default:` transient-error case) in
// service.go - the same mechanism pkg/lifecycle-poc/service_test.go's
// sourceRecoversAfterTransientError uses via a mock, just over the real
// in-memory stream transport instead.
func (p *recoverySourcePlugin) produceLoop(inmemStream *builtin.InMemorySourceRunStream, server pconnector.SourceRunStreamServer, start uint64) {
	pos := start
	for {
		next := pos + 1
		if p.failAt > 0 && next == p.failAt {
			inmemStream.Close(fmt.Errorf("chaos: injected transient recovery-test error at position %d", p.failAt))
			return
		}
		pos = next

		rec := makeRecord(pos, "") // recoverySourcePlugin never salts - single-source scenario, no collision to distinguish
		if err := server.Send(pconnector.SourceRunResponse{Records: []opencdc.Record{rec}}); err != nil {
			return // stream closed (process exiting, or Teardown ran)
		}
		printProgress("READ", pos)

		if p.total > 0 && pos >= p.total {
			return
		}
		if p.paceMS > 0 {
			time.Sleep(time.Duration(p.paceMS) * time.Millisecond)
		}
	}
}

// ackLoop drains ack messages and durably commits each one to upstreamStore,
// mirroring chaosPlugin.ackLoop's single-key mode: this is invariant 1's
// upstream side (the plugin's own irreversible commit), which
// pkg/connector.Source.Ack (source.go) only triggers once the resulting
// position is confirmed durably persisted by Conduit's own state store - see
// that method's own invariant-1 doc comment.
func (p *recoverySourcePlugin) ackLoop(server pconnector.SourceRunStreamServer) {
	for {
		req, err := server.Recv()
		if err != nil {
			return // stream closed
		}
		for _, pos := range req.AckPositions {
			n, err := decodePosition(pos)
			if err != nil {
				fmt.Fprintf(os.Stderr, "lc source plugin: invalid ack position %q: %v\n", pos, err)
				continue
			}
			if err := p.store.Commit(n); err != nil {
				fmt.Fprintf(os.Stderr, "lc source plugin: commit %d failed: %v\n", n, err)
				continue
			}
		}
	}
}

func (p *recoverySourcePlugin) Stop(context.Context, pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
	return pconnector.SourceStopResponse{}, nil
}

func (p *recoverySourcePlugin) Teardown(context.Context, pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	return pconnector.SourceTeardownResponse{}, nil
}

func (p *recoverySourcePlugin) LifecycleOnCreated(context.Context, pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	return pconnector.SourceLifecycleOnCreatedResponse{}, nil
}

func (p *recoverySourcePlugin) LifecycleOnUpdated(context.Context, pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	return pconnector.SourceLifecycleOnUpdatedResponse{}, nil
}

func (p *recoverySourcePlugin) LifecycleOnDeleted(context.Context, pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	return pconnector.SourceLifecycleOnDeletedResponse{}, nil
}

func (p *recoverySourcePlugin) NewStream() pconnector.SourceRunStream {
	return &builtin.InMemorySourceRunStream{}
}

// recoveryDestinationPlugin is this scenario's in-process
// pconnector.DestinationPlugin: it durably records each written record's
// position (via deliveryLog) BEFORE acking it back over the stream - see
// loop's doc comment for why that ordering is what makes this test's
// invariant-1 assertion structurally meaningful rather than a tautology.
type recoveryDestinationPlugin struct {
	log *deliveryLog
}

var _ pconnector.DestinationPlugin = (*recoveryDestinationPlugin)(nil)

func (p *recoveryDestinationPlugin) Configure(context.Context, pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	return pconnector.DestinationConfigureResponse{}, nil
}

func (p *recoveryDestinationPlugin) Open(context.Context, pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	return pconnector.DestinationOpenResponse{}, nil
}

func (p *recoveryDestinationPlugin) Run(ctx context.Context, stream pconnector.DestinationRunStream) error {
	inmemStream, ok := stream.(*builtin.InMemoryDestinationRunStream)
	if !ok {
		return fmt.Errorf("lc dest plugin: unexpected stream type %T", stream)
	}
	inmemStream.Init(ctx)
	server := inmemStream.Server()
	go p.loop(server)
	return nil
}

// loop drains write requests and, for every record, calls deliveryLog.Record
// (durable, fsynced) BEFORE including that position in the ack response it
// sends back. Because funnel.Worker only calls Source.Ack for positions this
// destination has already acked (funnel's Write-then-poll-Ack contract - see
// pkg/lifecycle-poc/funnel/destination.go), and Source.Ack in turn only tells
// the source plugin (recoverySourcePlugin.ackLoop, which commits to
// upstreamStore) once ITS OWN durable persist confirms - by construction, a
// position can never reach upstreamStore.Commit without first passing
// through this durable Record call. That is invariant 1's enforcement point
// for this synthetic pipeline: recovery_test.go's assertion checks the
// consequence (upstream watermark <= delivery log coverage) precisely because
// this ordering makes any violation of it a genuine engine bug, not an
// artifact of the test double.
func (p *recoveryDestinationPlugin) loop(server pconnector.DestinationRunStreamServer) {
	for {
		req, err := server.Recv()
		if err != nil {
			return // stream closed
		}
		acks := make([]pconnector.DestinationRunResponseAck, 0, len(req.Records))
		for _, r := range req.Records {
			if pos, err := decodePosition(r.Position); err == nil {
				if err := p.log.Record("", pos); err != nil { // single-source scenario: no sourceID to disambiguate
					fmt.Fprintf(os.Stderr, "%s: lc dest plugin: durable record failed: %v\n", markerFatal, err)
					os.Exit(exitOpenOtherError)
				}
			}
			acks = append(acks, pconnector.DestinationRunResponseAck{Position: r.Position})
		}
		if err := server.Send(pconnector.DestinationRunResponse{Acks: acks}); err != nil {
			return // stream closed
		}
	}
}

func (p *recoveryDestinationPlugin) Stop(context.Context, pconnector.DestinationStopRequest) (pconnector.DestinationStopResponse, error) {
	return pconnector.DestinationStopResponse{}, nil
}

func (p *recoveryDestinationPlugin) Teardown(context.Context, pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	return pconnector.DestinationTeardownResponse{}, nil
}

func (p *recoveryDestinationPlugin) LifecycleOnCreated(context.Context, pconnector.DestinationLifecycleOnCreatedRequest) (pconnector.DestinationLifecycleOnCreatedResponse, error) {
	return pconnector.DestinationLifecycleOnCreatedResponse{}, nil
}

func (p *recoveryDestinationPlugin) LifecycleOnUpdated(context.Context, pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	return pconnector.DestinationLifecycleOnUpdatedResponse{}, nil
}

func (p *recoveryDestinationPlugin) LifecycleOnDeleted(context.Context, pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	return pconnector.DestinationLifecycleOnDeletedResponse{}, nil
}

func (p *recoveryDestinationPlugin) NewStream() pconnector.DestinationRunStream {
	return &builtin.InMemoryDestinationRunStream{}
}

// lcSourceDispenser dispenses recoverySourcePlugin instances for this
// scenario. Only its FIRST dispense (per process) can be configured to
// inject the transient failAt error - every subsequent dispense (i.e. every
// restart driven by pkg/lifecycle-poc.Service's OWN recovery loop within this
// process) runs cleanly. This mirrors pkg/lifecycle-poc/service_test.go's
// sourceRecoversAfterTransientError: a real crash-recovery loop must
// eventually succeed once whatever caused the transient error clears - this
// test is about the recovery LOOP surviving a crash, not about repeated
// failure.
type lcSourceDispenser struct {
	store  *upstreamStore
	total  uint64
	paceMS int
	failAt uint64

	dispensed atomic.Int64
}

var _ connectorPlugin.Dispenser = (*lcSourceDispenser)(nil)

func (d *lcSourceDispenser) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("lcSourceDispenser: DispenseSpecifier not implemented")
}

func (d *lcSourceDispenser) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	failAt := uint64(0)
	if d.dispensed.Add(1) == 1 {
		failAt = d.failAt
	}
	return &recoverySourcePlugin{store: d.store, total: d.total, paceMS: d.paceMS, failAt: failAt}, nil
}

func (d *lcSourceDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return nil, cerrors.New("lcSourceDispenser: DispenseDestination not implemented")
}

// lcDestDispenser dispenses fresh recoveryDestinationPlugin instances backed
// by a single, fixed deliveryLog - used for both the main destination and the
// DLQ destination (each gets its own lcDestDispenser over its own log).
type lcDestDispenser struct {
	log *deliveryLog
}

var _ connectorPlugin.Dispenser = (*lcDestDispenser)(nil)

func (d *lcDestDispenser) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("lcDestDispenser: DispenseSpecifier not implemented")
}

func (d *lcDestDispenser) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	return nil, cerrors.New("lcDestDispenser: DispenseSource not implemented")
}

func (d *lcDestDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return &recoveryDestinationPlugin{log: d.log}, nil
}

// lcConnectorService fulfills pkg/lifecycle-poc.ConnectorService: a fixed,
// tiny, 3-entry (source/dest/DLQ) directory. Unlike the source's
// connector.Instance (loaded via loadOrCreateInstance so its position
// survives a restart, see buildLifecycleChild), the destination and DLQ
// instances are always fresh, in-memory objects for this process - matching
// how pkg/lifecycle-poc/service_test.go's dummyDestination is used, and
// consistent with this scenario's invariants, which are about SOURCE
// position durability and destination WRITE durability (deliveryLog), not
// about the destination connector.Instance's own bookkeeping.
type lcConnectorService struct {
	source *connector.Instance
	dest   *connector.Instance
	dlq    *connector.Instance
}

func (s lcConnectorService) Get(_ context.Context, id string) (*connector.Instance, error) {
	switch id {
	case s.source.ID:
		return s.source, nil
	case s.dest.ID:
		return s.dest, nil
	case s.dlq.ID:
		return s.dlq, nil
	default:
		return nil, connector.ErrInstanceNotFound
	}
}

func (s lcConnectorService) Create(context.Context, string, connector.Type, string, string, connector.Config, connector.ProvisionType) (*connector.Instance, error) {
	return s.dlq, nil
}

// WaitPersisted satisfies the ConnectorService interface (widened for arch-v2
// StopAndWait's durability barrier). This crash scenario never calls StopAndWait
// — it SIGKILLs the process — so there is nothing to wait on; the real
// persister's durability is what the test asserts against post-restart.
func (s lcConnectorService) WaitPersisted() {}

// lcProcessorService fulfills pkg/lifecycle-poc.ProcessorService. This
// scenario's pipeline has no processors (pl.ProcessorIDs is always empty), so
// neither method is ever actually invoked - present only to satisfy the
// interface, exactly like pkg/lifecycle-poc/service_test.go's
// testProcessorService.
type lcProcessorService struct{}

func (lcProcessorService) Get(context.Context, string) (*processor.Instance, error) {
	return nil, cerrors.New("lcProcessorService: not implemented")
}

func (lcProcessorService) MakeRunnableProcessor(context.Context, *processor.Instance) (*processor.RunnableProcessor, error) {
	return nil, cerrors.New("lcProcessorService: not implemented")
}

// lcPipelineService fulfills pkg/lifecycle-poc.PipelineService for a single,
// fixed pipeline.Instance built once at process startup (never persisted -
// see buildLifecycleChild's doc for why pipeline metadata durability is out
// of scope for this test).
type lcPipelineService struct {
	mu  sync.Mutex
	pls map[string]*pipeline.Instance
}

func newLCPipelineService(pl *pipeline.Instance) *lcPipelineService {
	return &lcPipelineService{pls: map[string]*pipeline.Instance{pl.ID: pl}}
}

func (s *lcPipelineService) Get(_ context.Context, id string) (*pipeline.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pls[id]
	if !ok {
		return nil, cerrors.Errorf("lc pipeline service: pipeline %s not found", id)
	}
	return p, nil
}

func (s *lcPipelineService) List(context.Context) map[string]*pipeline.Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*pipeline.Instance, len(s.pls))
	for k, v := range s.pls {
		out[k] = v
	}
	return out
}

// UpdateStatus applies the status to the tracked pipeline.Instance and prints
// a markerLCStatus line - see that constant's doc for why this is the only
// channel available to tell the parent test process which lifecycle status
// was just reached.
func (s *lcPipelineService) UpdateStatus(_ context.Context, id string, status pipeline.Status, errMsg string) error {
	s.mu.Lock()
	p, ok := s.pls[id]
	s.mu.Unlock()
	if !ok {
		return cerrors.Errorf("lc pipeline service: pipeline %s not found", id)
	}
	p.SetStatus(status)
	p.Error = errMsg
	fmt.Printf("%s %s\n", markerLCStatus, status)
	return nil
}

// childLifecycleBuilt bundles the real engine pieces buildLifecycleChild
// wires together for one child run - the pkg/lifecycle-poc.Service analogue
// of child.go's childBuilt.
type childLifecycleBuilt struct {
	logger    log.CtxLogger
	persister *connector.Persister
	store     *connector.Store
	upstream  *upstreamStore
	mainLog   *deliveryLog
	dlqLog    *deliveryLog

	sourceInstance *connector.Instance
	destInstance   *connector.Instance
	dlqInstance    *connector.Instance

	connectors       lcConnectorService
	connectorPlugins staticFetcher
	pipelines        *lcPipelineService

	pipelineID string
}

// buildLifecycleChild opens (or resumes) the badger DB at cfg.dbDir - the
// exact same durability boundary buildChild uses, reused so this scenario
// composes on top of it rather than reinventing it - plus the upstream
// commit ledger and both delivery logs, then wires a full, real
// pkg/lifecycle-poc.Service around a real source/destination/DLQ built from
// this scenario's own plugins.
func buildLifecycleChild(ctx context.Context, cfg lcChildEnv) (*childLifecycleBuilt, error) {
	logger := log.New(zerolog.Nop()) // keep stdout clean; it is our marker/progress channel

	db, err := badger.New(zerolog.Nop(), cfg.dbDir)
	if err != nil {
		return nil, fmt.Errorf("open badger db: %w", err)
	}

	// A short debounce (unlike the connector-layer sigkill tests, which use
	// the production-matching DefaultPersisterDelayThreshold to time a kill
	// relative to that specific window): this scenario's crash windows are
	// the RECOVERY LOOP's own (parked in the backoff wait / mid the
	// recovered run, see recovery_test.go), not the ack->persist debounce
	// window sigkill_test.go already covers exhaustively, so fast,
	// near-immediate flushing keeps this test quick without weakening what
	// it proves.
	persister := connector.NewPersister(logger, db, lcPersistDelay, 1)
	store := connector.NewStore(db, logger)

	sourceInstance := loadOrCreateInstance(ctx, store)
	sourceInstance.Init(logger, persister)

	upstream, err := openUpstreamStore(cfg.upstreamDir, false) // durable/Kafka-like: this scenario is not about prune/gap semantics, see sigkill_test.go for that
	if err != nil {
		return nil, err
	}
	mainLog, err := openDeliveryLog(cfg.mainDir)
	if err != nil {
		return nil, err
	}
	dlqLog, err := openDeliveryLog(cfg.dlqDir)
	if err != nil {
		return nil, err
	}

	destInstance := &connector.Instance{
		ID:            lcDestInstanceID,
		Type:          connector.TypeDestination,
		PipelineID:    lcPipelineID,
		Plugin:        lcDestPlugin,
		Config:        connector.Config{Name: lcDestInstanceID, Settings: map[string]string{}},
		ProvisionedBy: connector.ProvisionTypeAPI,
	}
	destInstance.Init(logger, persister)

	dlqInstance := &connector.Instance{
		ID:            lcDLQInstanceID,
		Type:          connector.TypeDestination,
		PipelineID:    lcPipelineID,
		Plugin:        lcDLQPlugin,
		Config:        connector.Config{Name: lcDLQInstanceID, Settings: map[string]string{}},
		ProvisionedBy: connector.ProvisionTypeDLQ,
	}
	dlqInstance.Init(logger, persister)

	pl := &pipeline.Instance{
		ID:           lcPipelineID,
		Config:       pipeline.Config{Name: lcPipelineID},
		DLQ:          pipeline.DLQ{Plugin: lcDLQPlugin, Settings: map[string]string{}, WindowSize: 1, WindowNackThreshold: 0},
		ConnectorIDs: []string{sourceInstance.ID, destInstance.ID},
	}
	pl.SetStatus(pipeline.StatusUserStopped) // Init's own required starting status, per pkg/lifecycle-poc.Service.Start

	plugins := staticFetcher{
		sourceInstance.Plugin: &lcSourceDispenser{store: upstream, total: cfg.total, paceMS: cfg.paceMS, failAt: cfg.failAt},
		destInstance.Plugin:   &lcDestDispenser{log: mainLog},
		dlqInstance.Plugin:    &lcDestDispenser{log: dlqLog},
	}

	return &childLifecycleBuilt{
		logger:           logger,
		persister:        persister,
		store:            store,
		upstream:         upstream,
		mainLog:          mainLog,
		dlqLog:           dlqLog,
		sourceInstance:   sourceInstance,
		destInstance:     destInstance,
		dlqInstance:      dlqInstance,
		connectors:       lcConnectorService{source: sourceInstance, dest: destInstance, dlq: dlqInstance},
		connectorPlugins: plugins,
		pipelines:        newLCPipelineService(pl),
		pipelineID:       pl.ID,
	}, nil
}

// waitForLCTotalAcked blocks (polling, not sleeping a fixed duration) until
// the source connector's own in-memory State shows it has acked through
// position total, or exits the process (markerFatal) after a generous
// timeout. Mirrors pkg/lifecycle-poc/service_test.go's waitForRecordsAcked,
// adapted for a plain child-process program (no *testing.T here).
func waitForLCTotalAcked(instance *connector.Instance, total uint64, timeout time.Duration) {
	if total == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		instance.RLock()
		state, ok := instance.State.(connector.SourceState)
		instance.RUnlock()
		if ok {
			if pos, err := decodePosition(state.Position); err == nil && pos >= total {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "%s: timed out waiting for source to ack through position %d\n", markerFatal, total)
	os.Exit(exitOpenOtherError)
}

// runChildLifecycle is this file's entire child-process program (see the
// file doc comment). It never returns - it always calls os.Exit, either
// immediately after blocking forever (the crashable variant, cfg.graceful ==
// false: the parent SIGKILLs this process at a marker-gated moment and this
// code never runs again) or after a graceful stop (cfg.graceful == true).
func runChildLifecycle() {
	ctx := context.Background()
	cfg := parseLCChildEnv()

	built, err := buildLifecycleChild(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", markerFatal, err)
		os.Exit(exitBadArgs)
	}

	errRecoveryCfg := &lifecyclev1.ErrRecoveryCfg{
		MinDelay:         time.Duration(cfg.minDelayMS) * time.Millisecond,
		MaxDelay:         time.Duration(cfg.maxDelayMS) * time.Millisecond,
		BackoffFactor:    2,
		MaxRetries:       lifecyclev1.InfiniteRetriesErrRecovery,
		MaxRetriesWindow: time.Minute,
	}

	ls := lifecycle.NewService(
		built.logger,
		errRecoveryCfg,
		built.connectors,
		lcProcessorService{},
		built.connectorPlugins,
		built.pipelines,
		true, // metricsDisabled: no Prometheus registry in this child process
	)

	if err := ls.Start(ctx, built.pipelineID); err != nil {
		fmt.Fprintf(os.Stderr, "%s: start failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}

	if !cfg.graceful {
		// Crashable variant, used by both of recovery_test.go's SIGKILL
		// scenarios: block forever. The parent watches this process's
		// markerLCStatus/READ progress lines on stdout and SIGKILLs it at
		// whatever point it chose (parked in the recovery backoff wait, or
		// mid the recovered run) - no graceful path (Worker.Stop,
		// Source.Teardown) ever runs from here on in this process, which is
		// the entire point (see doc.go and this file's own doc comment).
		select {}
	}

	// Graceful "run to completion" variant: used for the post-crash restart
	// that proves the pipeline finishes end-to-end (invariant 3,
	// at-least-once) despite the earlier kill. Resumes from whatever position
	// was durably persisted by the killed process and runs to cfg.total.
	waitForLCTotalAcked(built.sourceInstance, cfg.total, 30*time.Second)

	if err := ls.Stop(ctx, built.pipelineID, false); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stop failed: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	if err := ls.WaitPipeline(built.pipelineID); err != nil {
		fmt.Fprintf(os.Stderr, "%s: pipeline stopped with error: %v\n", markerFatal, err)
		os.Exit(exitOpenOtherError)
	}
	built.persister.WaitPendingWrites()

	// waitForLCTotalAcked/WaitPipeline only guarantee the last ack was HANDED
	// OFF to the plugin (recoverySourcePlugin.ackLoop's server.Recv), not that
	// ackLoop's subsequent side effect (upstreamStore.Commit) has itself run
	// yet - the exact same gap runChild's own tail comment (child.go) warns
	// about for chaosPlugin. Without this wait, this process could print
	// markerLCDone and exit while that final commit is still in flight,
	// producing a false-positive "off by one" gap that has nothing to do with
	// the recovery loop under test.
	// Use the SAME lcPersistDelay this file's buildLifecycleChild actually
	// configured connector.NewPersister with (see lcPersistDelay's doc for
	// why this must not be connector.DefaultPersisterDelayThreshold, which
	// is what a stale comment here used to claim and what this call used to
	// pass).
	waitForUpstreamCommitted(built.upstream, cfg.total, lcPersistDelay)

	fmt.Println(markerLCDone)
	os.Exit(exitOK)
}
