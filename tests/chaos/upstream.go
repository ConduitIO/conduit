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
	"sync"
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
type chaosPlugin struct {
	store  *upstreamStore
	total  uint64 // 0 means unbounded
	paceMS int    // delay between produced records; 0 = as-fast-as-possible burst

	mu         sync.Mutex
	nextToRead uint64 // set by Open, read by the producer goroutine
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
			committed, resume, committed-resume)
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

	go p.produceLoop(server, start)
	go p.ackLoop(server)
	return nil
}

// produceLoop delivers records start+1, start+2, ... up to p.total
// (inclusive), pacing each send by p.paceMS. It never re-checks Committed —
// producing is independent of committing, exactly like a real source
// connector's read loop runs concurrently with (and ahead of) acks arriving
// for records it already sent.
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

		if p.paceMS > 0 {
			time.Sleep(time.Duration(p.paceMS) * time.Millisecond)
		}
	}
}

// ackLoop drains ack messages and durably commits each one. It also emits a
// progress line to stdout right after the durable commit completes, so the
// parent test process can wait for a specific number of DURABLE commits
// (rather than guessing with a fixed sleep) before deciding when to SIGKILL —
// per the design doc's flakiness-mitigation guidance to prefer a record-count/
// log-line trigger over a wall-clock sleep guess where it's this cheap to do.
func (p *chaosPlugin) ackLoop(server pconnector.SourceRunStreamServer) {
	for {
		req, err := server.Recv()
		if err != nil {
			return // stream closed
		}
		for _, pos := range req.AckPositions {
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

// printProgress emits a machine-parseable progress line to stdout. os.Stdout
// writes go straight to the underlying file descriptor (fmt does no
// buffering of its own), so this reaches the parent process's pipe
// immediately - the parent reads these to know precisely how many positions
// have been durably committed upstream, instead of guessing with a fixed
// sleep. See the design doc's flakiness-mitigation guidance.
func printProgress(tag string, pos uint64) {
	fmt.Printf("%s %d\n", tag, pos)
}
