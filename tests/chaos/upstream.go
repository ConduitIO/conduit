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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// upstreamStore models the durable external system a source connector reads
// from and commits records to — e.g. Debezium reading Postgres's replication
// slot. Its "committed" watermark is written to disk and fsynced
// SYNCHRONOUSLY on every commit, deliberately unlike Conduit's own
// Persister (persister.go), which debounces. That asymmetry is the point:
// this store models a plugin/upstream whose own commit is immediate and
// irreversible from Conduit's perspective, exactly as pkg/connector/source.go
// assumes when it sends the ack to the plugin before persisting its own
// state.
//
// If prune is true, the store also models a replication slot whose WAL
// segments are recycled once confirmed: Committed() reports the highest
// position ever committed across ALL runs (including ones a SIGKILL cut
// short), and the chaosPlugin's Open refuses to resume from anywhere behind
// it. If prune is false, the store never restricts where a resume can start
// from — the modeled upstream is a durable, replayable log (e.g. Kafka) that
// can redeliver from an arbitrarily old offset.
type upstreamStore struct {
	path  string
	prune bool

	mu sync.Mutex
}

func openUpstreamStore(dir string, prune bool) (*upstreamStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("upstreamStore: mkdir %s: %w", dir, err)
	}
	return &upstreamStore{path: filepath.Join(dir, "committed"), prune: prune}, nil
}

// Committed returns the highest position ever durably committed, read fresh
// from disk every call — so a freshly restarted process picks up whatever a
// previous, killed process last committed. Returns 0 if nothing was ever
// committed.
func (u *upstreamStore) Committed() (uint64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.readLocked()
}

func (u *upstreamStore) readLocked() (uint64, error) {
	raw, err := os.ReadFile(u.path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("upstreamStore: read %s: %w", u.path, err)
	}
	n, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		// A torn/corrupted read of the upstream's OWN commit marker is not
		// what this workstream's invariant-2 assertion is about (that's
		// Conduit's persisted position, checked independently in
		// child.go) — but it should never happen given the synchronous
		// write+fsync below, so surface it loudly rather than silently
		// treating it as "nothing committed".
		return 0, fmt.Errorf("upstreamStore: corrupt commit marker %q in %s: %w", raw, u.path, err)
	}
	return n, nil
}

// Commit durably records that pos has been committed upstream (Debezium's
// task.commitRecord+task.commit, in the real wrapper). It is synchronous and
// fsynced before returning — modeling an upstream commit that is immediate
// and durable, unlike Conduit's own debounced Persister.
func (u *upstreamStore) Commit(pos uint64) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	cur, err := u.readLocked()
	if err != nil {
		return err
	}
	if pos <= cur {
		return nil // already committed at least this far; idempotent
	}

	tmp := u.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("upstreamStore: open %s: %w", tmp, err)
	}
	if _, err := fmt.Fprintf(f, "%d", pos); err != nil {
		_ = f.Close()
		return fmt.Errorf("upstreamStore: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("upstreamStore: fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("upstreamStore: close %s: %w", tmp, err)
	}
	// Atomic rename: the marker file is never observed half-written.
	if err := os.Rename(tmp, u.path); err != nil {
		return fmt.Errorf("upstreamStore: rename %s -> %s: %w", tmp, u.path, err)
	}
	return nil
}

// chaosPlugin is a minimal, in-process pconnector.SourcePlugin standing in
// for the wrapper's DefaultSourceStream. It produces a deterministic stream
// of records (position N's payload is always "record-N", so "was record N
// ever delivered" needs no separate ledger — it's a pure function of
// position) and, on ack, durably commits to upstream via upstreamStore.
//
// DBZ-2 (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md)
// generalizes this single-phase producer two ways, both opt-in via
// zero-valued fields so DBZ-1's existing single-key, single-pace scenarios
// (sigkill_test.go) are byte-for-byte unaffected:
//
//   - Property 1/2 (snapshot->stream continuity + its SIGKILL timings): if
//     snapshotK > 0, positions 1..snapshotK are produced at snapshotPaceMS
//     (a fast burst, mirroring Debezium's initial full-table dump) and
//     positions snapshotK+1..total at paceMS (steady-state pacing), with a
//     "HANDOFF <snapshotK>" marker printed the instant the producer crosses
//     the boundary. See produceLoop and doc.go/the design doc's Property 1
//     section for why this boundary is a producer-pacing device for
//     kill-timing, NOT evidence of a persisted engine "handoff" state — the
//     engine stores one opaque monotone position regardless.
//   - Property 3 (per-partition ordering): if numKeys > 1, records are
//     produced round-robin across numKeys synthetic partition keys, each
//     key's position independently monotone ("k<i>:<seq>", see
//     encodeKeyedPosition). This is a fundamentally different scenario than
//     Property 1/2's SIGKILL cases: it never restarts and never touches
//     upstreamStore's commit/prune/gap machinery (per-partition ordering is
//     an ack-*delivery-order* guarantee, not a crash-recovery one - see the
//     design doc's Property 3 section) - see ackLoop's ACK_ORDER lines,
//     which are the real per-key delivery ledger the parent test asserts on.
type chaosPlugin struct {
	store  *upstreamStore
	total  uint64 // 0 means unbounded
	paceMS int    // delay between produced records once past snapshotK; 0 = as-fast-as-possible burst

	// snapshotK and snapshotPaceMS are Property 1/2's two-phase producer
	// knobs (see the type doc above). snapshotK == 0 means "no distinct
	// phase" - every position is produced at paceMS, identical to DBZ-1's
	// original single-phase behavior.
	snapshotK      uint64
	snapshotPaceMS int

	// numKeys is Property 3's multi-key knob (see the type doc above).
	// numKeys <= 1 means "single, unkeyed position space" - identical to
	// DBZ-1's original behavior (plain "N" positions, upstreamStore commits
	// tracked by a single watermark).
	numKeys int

	mu         sync.Mutex
	nextToRead uint64 // set by Open, read by the producer goroutine (single-key mode only, see Open)

	// ackedCount is Property 3's substitute for upstreamStore's synchronous
	// commit-watermark file: in multi-key mode ackLoop never calls
	// store.Commit (there is no single watermark to commit - see ackLoop),
	// so runChild's graceful-exit path needs a different, equally-precise
	// signal that every position has actually had its ACK_ORDER line
	// printed before the process exits and the parent test reads stdout.
	// Incremented by ackLoop strictly after each ACK_ORDER print - see
	// waitForPluginAckedCount's doc for why this can't just be inferred
	// from Persister.WaitPendingWrites returning.
	ackedCount atomic.Uint64
}

var _ pconnector.SourcePlugin = (*chaosPlugin)(nil)

func (p *chaosPlugin) Configure(context.Context, pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	return pconnector.SourceConfigureResponse{}, nil
}

// Open is where the crash window's consequence becomes observable: if the
// upstream prunes (Postgres-slot-like) and Conduit asks to resume from
// behind the already-committed watermark, this returns a hard, loud error —
// modeling Postgres's real "requested WAL segment has already been removed"
// behavior, not a silent skip. See doc.go for why gap-vs-duplicate depends on
// this flag.
func (p *chaosPlugin) Open(_ context.Context, req pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	if p.numKeys > 1 {
		// Property 3 (per-partition ordering) never restarts and its keyed
		// "k<i>:<seq>" position encoding is not the single global counter the
		// prune/gap logic below assumes - there is nothing to resume from and
		// no gap semantics to apply. Every key's sequence always starts at 1
		// (see produceLoop).
		return pconnector.SourceOpenResponse{}, nil
	}

	resume, err := decodePosition(req.Position)
	if err != nil {
		return pconnector.SourceOpenResponse{}, fmt.Errorf("chaos plugin: invalid resume position %q: %w", req.Position, err)
	}

	committed, err := p.store.Committed()
	if err != nil {
		return pconnector.SourceOpenResponse{}, err
	}

	if p.store.prune && resume < committed {
		return pconnector.SourceOpenResponse{}, fmt.Errorf(
			"GAP: chaos upstream already committed/pruned through position %d, but Conduit asked to "+
				"resume from position %d — the %d position(s) in between are no longer available upstream "+
				"(modeling a Postgres replication slot whose WAL for already-confirmed positions was recycled)",
			committed, resume, committed-resume,
		)
	}

	p.mu.Lock()
	p.nextToRead = resume
	p.mu.Unlock()
	return pconnector.SourceOpenResponse{}, nil
}

func (p *chaosPlugin) Run(ctx context.Context, stream pconnector.SourceRunStream) error {
	inmemStream, ok := stream.(*builtin.InMemorySourceRunStream)
	if !ok {
		return fmt.Errorf("chaos plugin: unexpected stream type %T", stream)
	}
	inmemStream.Init(ctx)
	server := inmemStream.Server()

	p.mu.Lock()
	start := p.nextToRead
	p.mu.Unlock()

	if p.numKeys > 1 {
		go p.produceLoopKeyed(server)
	} else {
		go p.produceLoop(server, start)
	}
	go p.ackLoop(server)
	return nil
}

// produceLoop delivers records start+1, start+2, ... up to p.total
// (inclusive). It never re-checks Committed — producing is independent of
// committing, exactly like a real source connector's read loop runs
// concurrently with (and ahead of) acks arriving for records it already
// sent.
//
// Property 1/2's two-phase pacing (docs/design-documents/
// 20260726-dbz2-cdc-correctness-suite.md): if snapshotK > 0, positions
// 1..snapshotK are paced at snapshotPaceMS (a fast "snapshot" burst) and
// snapshotK+1..total at paceMS (steady-state "stream" pacing) — mirroring
// Debezium's initial full-table dump followed by paced logical replication.
// snapshotK == 0 (DBZ-1's original single-phase behavior) paces every
// position at paceMS unconditionally. The instant the producer crosses the
// boundary (position snapshotK itself), a "HANDOFF <snapshotK>" marker is
// printed — see the type doc for why this is a kill-timing device for
// Property 2's mid-handoff case, not evidence of a persisted engine state.
//
// It prints a "READ <pos>" progress line right after each successful send —
// deliberately NOT gated on any ack/commit/flush behavior, unlike "ACK" lines
// (see ackLoop). Approach A (docs/design-documents/
// 20260723-source-ack-persist-ordering-fix.md) defers the plugin's ack
// visibility behind connector.Persister's debounce, so "ACK" progress no
// longer tracks wall-clock-since-start at a fine grain for a fast burst — it
// can arrive in one lump once a flush fires. "READ" progress does not have
// that problem: production is paced deterministically, so harness.go's
// kill-timing waits on READ (and, for mid-handoff, HANDOFF) lines, never ACK
// lines.
func (p *chaosPlugin) produceLoop(server pconnector.SourceRunStreamServer, start uint64) {
	pos := start
	for {
		if p.total > 0 && pos >= p.total {
			return
		}
		pos++

		rec := makeRecord(pos)
		if err := server.Send(pconnector.SourceRunResponse{Records: []opencdc.Record{rec}}); err != nil {
			return // stream closed (process exiting, or Teardown ran)
		}
		printProgress("READ", pos)

		if p.snapshotK > 0 && pos == p.snapshotK {
			// Crossed the snapshot->stream boundary: from here on, pacing
			// switches from snapshotPaceMS to paceMS (see the loop body
			// below). The marker is printed AFTER this position's READ line
			// and BEFORE the pacing sleep, so a parent waiting on it
			// (waitForMarker) observes it exactly once this position has
			// been fully produced - never early.
			fmt.Printf("%s %d\n", markerHandoff, pos)
		}

		pace := p.paceMS
		if p.snapshotK > 0 && pos < p.snapshotK {
			pace = p.snapshotPaceMS
		}
		if pace > 0 {
			time.Sleep(time.Duration(pace) * time.Millisecond)
		}
	}
}

// produceLoopKeyed is Property 3's multi-key producer: it round-robins
// across p.numKeys synthetic partition keys, each key's own sequence
// starting at 1 and advancing independently of the others (key i's Nth
// record has position "k<i>:<N>" - see encodeKeyedPosition). Unlike
// produceLoop, this never restarts (Property 3 is a single, no-crash run —
// see the type doc), so there is no "start" resume offset to honor.
//
// paceMS (not snapshotK/snapshotPaceMS - the two-phase snapshot/stream
// concept is Property 1/2's, not Property 3's) is used unconditionally to
// pace records, chosen by the test to be long enough that the run crosses
// multiple connector.Persister debounce windows (DefaultPersisterDelayThreshold,
// 1s) — so the per-key ordering assertion in ordering_test.go actually
// exercises "no reorder across a debounce flush", not just a single
// end-of-run flush.
func (p *chaosPlugin) produceLoopKeyed(server pconnector.SourceRunStreamServer) {
	// numKeys is always a small, positive, harness-supplied constant (Run
	// only calls this method when p.numKeys > 1 - see Run) - the uint64
	// conversion below can never see a negative value in practice, but
	// gosec can't see that invariant, hence the explicit nolint.
	numKeys := uint64(p.numKeys) //nolint:gosec // p.numKeys > 1 is enforced by Run's caller before this method is ever invoked
	keySeq := make([]uint64, p.numKeys)
	var produced uint64
	for {
		if p.total > 0 && produced >= p.total {
			return
		}
		key := int(produced % numKeys) //nolint:gosec // result is < numKeys, a small positive int (see above), always representable as int
		keySeq[key]++
		produced++

		rec := makeKeyedRecord(key, keySeq[key])
		if err := server.Send(pconnector.SourceRunResponse{Records: []opencdc.Record{rec}}); err != nil {
			return // stream closed (process exiting, or Teardown ran)
		}
		printProgressStr("READ", string(rec.Position))

		if p.paceMS > 0 {
			time.Sleep(time.Duration(p.paceMS) * time.Millisecond)
		}
	}
}

// ackLoop drains ack messages and, in single-key mode, durably commits each
// one to upstreamStore (see the type doc's Property 1/2 vs Property 3
// split). It always prints an "ACK_ORDER <arrival> <pos>" line per acked
// position — arrival is a per-process, strictly increasing counter assigned
// in the exact order this loop's server.Recv() call returned it, i.e. the
// exact order pkg/connector/source.go's onPersistFlushed called
// sendDeferredAck. This is Property 3's real per-key delivery ledger (see
// ordering_test.go): unlike a max-position counter, it lets the parent test
// detect intra-batch reordering, not just gaps.
//
// In single-key mode it ALSO keeps emitting the original "ACK <n>" line and
// durable Commit(n) call unchanged, so DBZ-1's existing gap/duplicate
// assertions (sigkill_test.go) are unaffected by ACK_ORDER's addition.
func (p *chaosPlugin) ackLoop(server pconnector.SourceRunStreamServer) {
	var arrival uint64
	for {
		req, err := server.Recv()
		if err != nil {
			return // stream closed
		}
		arrival++
		for _, pos := range req.AckPositions {
			fmt.Printf("%s %d %s\n", markerAckOrder, arrival, pos)
			p.ackedCount.Add(1)

			if p.numKeys > 1 {
				continue // Property 3: no single watermark to commit, see type doc
			}
			n, err := decodePosition(pos)
			if err != nil {
				fmt.Fprintf(os.Stderr, "chaos plugin: invalid ack position %q: %v\n", pos, err)
				continue
			}
			if err := p.store.Commit(n); err != nil {
				fmt.Fprintf(os.Stderr, "chaos plugin: commit %d failed: %v\n", n, err)
				continue
			}
			printProgress("ACK", n)
		}
	}
}

func (p *chaosPlugin) Stop(context.Context, pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
	return pconnector.SourceStopResponse{}, nil
}

func (p *chaosPlugin) Teardown(context.Context, pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	return pconnector.SourceTeardownResponse{}, nil
}

func (p *chaosPlugin) LifecycleOnCreated(context.Context, pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	return pconnector.SourceLifecycleOnCreatedResponse{}, nil
}

func (p *chaosPlugin) LifecycleOnUpdated(context.Context, pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	return pconnector.SourceLifecycleOnUpdatedResponse{}, nil
}

func (p *chaosPlugin) LifecycleOnDeleted(context.Context, pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	return pconnector.SourceLifecycleOnDeletedResponse{}, nil
}

func (p *chaosPlugin) NewStream() pconnector.SourceRunStream {
	return &builtin.InMemorySourceRunStream{}
}

// makeRecord deterministically builds the record for position pos: "was
// record N ever delivered end-to-end" therefore needs no separate ledger —
// it's answerable purely from the position, both here and in the parent
// test's final verification.
func makeRecord(pos uint64) opencdc.Record {
	return opencdc.Record{
		Position:  encodePosition(pos),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.RawData(fmt.Sprintf("key-%d", pos)),
		Payload:   opencdc.Change{After: opencdc.RawData(fmt.Sprintf("record-%d", pos))},
	}
}

func encodePosition(pos uint64) opencdc.Position {
	return opencdc.Position(strconv.FormatUint(pos, 10))
}

// decodePosition returns 0 for a nil/empty position (Conduit's Source.Open
// with no persisted state, i.e. a genuinely fresh start).
func decodePosition(p opencdc.Position) (uint64, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return strconv.ParseUint(string(p), 10, 64)
}

// makeKeyedRecord deterministically builds Property 3's multi-key record for
// partition key keyIdx's seq'th record (seq is 1-based, monotone within
// keyIdx - see produceLoopKeyed). Like makeRecord, the payload is a pure
// function of (keyIdx, seq), and the position ITSELF encodes both (see
// encodeKeyedPosition) — so the parent test's per-key delivery ledger
// (ordering_test.go) never needs to open the badger DB or upstreamStore; it
// reads everything it needs off the ACK_ORDER stdout lines.
func makeKeyedRecord(keyIdx int, seq uint64) opencdc.Record {
	key := fmt.Sprintf("key-%d", keyIdx)
	return opencdc.Record{
		Position:  encodeKeyedPosition(keyIdx, seq),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.RawData(key),
		Payload:   opencdc.Change{After: opencdc.RawData(fmt.Sprintf("%s-record-%d", key, seq))},
	}
}

// encodeKeyedPosition encodes Property 3's multi-key position as
// "k<keyIdx>:<seq>" — deliberately still an opaque byte string as far as
// pkg/connector.Source is concerned (it never parses Position contents, see
// source.go:78-90), but self-describing to THIS test harness, which controls
// both the encoding and the only code that ever decodes it
// (decodeKeyedPosition, ordering_test.go).
func encodeKeyedPosition(keyIdx int, seq uint64) opencdc.Position {
	return opencdc.Position(fmt.Sprintf("k%d:%d", keyIdx, seq))
}

// decodeKeyedPosition is encodeKeyedPosition's inverse, used by
// ordering_test.go to rebuild the per-key delivery ledger from ACK_ORDER
// lines.
func decodeKeyedPosition(p opencdc.Position) (keyIdx int, seq uint64, err error) {
	s := string(p)
	i := strings.IndexByte(s, ':')
	if len(s) < 2 || s[0] != 'k' || i < 0 {
		return 0, 0, fmt.Errorf("chaos plugin: malformed keyed position %q", s)
	}
	keyIdx, err = strconv.Atoi(s[1:i])
	if err != nil {
		return 0, 0, fmt.Errorf("chaos plugin: malformed keyed position %q: %w", s, err)
	}
	seq, err = strconv.ParseUint(s[i+1:], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("chaos plugin: malformed keyed position %q: %w", s, err)
	}
	return keyIdx, seq, nil
}

// printProgress emits a machine-parseable progress line to stdout. os.Stdout
// writes go straight to the underlying file descriptor (fmt does no
// buffering of its own), so this reaches the parent process's pipe
// immediately - the parent reads these to know precisely how many positions
// have been durably committed upstream, instead of guessing with a fixed
// sleep. See the design doc's flakiness-mitigation guidance.
func printProgress(tag string, pos uint64) {
	fmt.Printf("%s %d\n", tag, pos)
}

// printProgressStr is printProgress's counterpart for Property 3's keyed,
// non-numeric positions.
func printProgressStr(tag string, pos string) {
	fmt.Printf("%s %s\n", tag, pos)
}

// waitForAckedCount blocks (polling, like childProcess.waitForReadCount)
// until at least n positions have had their ACK_ORDER line printed by
// ackLoop, or returns false once timeout has elapsed. See ackedCount's field
// doc for why Property 3's graceful-exit path needs this instead of (or in
// addition to) Persister.WaitPendingWrites: that only guarantees
// onPersistFlushed's synchronous stream.Send rendezvous'd with ackLoop's
// Recv, not that ackLoop's goroutine has gone on to execute the ACK_ORDER
// print statement after it.
func (p *chaosPlugin) waitForAckedCount(n uint64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.ackedCount.Load() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return p.ackedCount.Load() >= n
}
