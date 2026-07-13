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

package stream

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// recordingProc is a hand-written Processor (not a gomock) for the property and
// fault tests, where random interleavings and blocking-mid-Open control are
// awkward to express as up-front gomock expectations. It tags each record with
// its version, records the size of every Process call, and can block in Open.
type recordingProc struct {
	version int

	mu        sync.Mutex
	callSizes []int // len(recs) of each Process call, to pin the 1-in-1-out contract

	// reachedOpen, if non-nil, is closed when Open is entered; blockOpen makes
	// Open then block until ctx is done (to hold a swap open mid-Open).
	reachedOpen chan struct{}
	blockOpen   bool
}

func (f *recordingProc) Open(ctx context.Context) error {
	if f.reachedOpen != nil {
		close(f.reachedOpen)
	}
	if f.blockOpen {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *recordingProc) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	f.mu.Lock()
	f.callSizes = append(f.callSizes, len(recs))
	f.mu.Unlock()
	out := make([]sdk.ProcessedRecord, len(recs))
	for i, r := range recs {
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		r.Metadata["version"] = strconv.Itoa(f.version)
		out[i] = sdk.SingleRecord(r)
	}
	return out
}

func (f *recordingProc) Teardown(context.Context) error { return nil }

// TestProcessorNode_OneRecordPerProcessCall_LiveSwapContract pins the non-buffering
// 1-in-1-out processor contract the live-swap zero-drop guarantee depends on (see
// the design doc's Data-safety §): the node must hand Process exactly one record
// per call and never batch. If a future change makes the node call Process with
// more than one record (a batching/windowing model), this test breaks — forcing
// whoever makes that change to confront the hot-swap interaction (a buffered
// processor must be restart-class, or flush before teardown) rather than
// silently introducing a drop.
func TestProcessorNode_OneRecordPerProcessCall_LiveSwapContract(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	const n = 8
	proc := &recordingProc{version: 0}
	node := &ProcessorNode{Name: "t", Processor: proc, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	node.Sub(in)
	out := node.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); is.NoErr(node.Run(ctx)) }()

	go func() {
		for i := 0; i < n; i++ {
			in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte(strconv.Itoa(i)), Metadata: map[string]string{}}}
		}
		close(in)
	}()
	for i := 0; i < n; i++ {
		<-out
	}
	wg.Wait()

	proc.mu.Lock()
	defer proc.mu.Unlock()
	is.Equal(len(proc.callSizes), n) // one Process call per record
	for _, sz := range proc.callSizes {
		is.Equal(sz, 1) // never batched — the contract the live swap relies on
	}
}

// TestProcessorNode_Reconfigure_PreservesOrderingAndDelivery is the property-style
// test: it interleaves a stream of records with many live reconfigures and
// asserts every record comes out exactly once, in order (invariants 3 and 4). The
// version that tags any given record is nondeterministic (it depends on which
// swap won the race at that boundary) — that is expected; what must hold is that
// no record is dropped, duplicated, or reordered across the swaps.
func TestProcessorNode_Reconfigure_PreservesOrderingAndDelivery(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	const n = 200
	node := &ProcessorNode{Name: "t", Processor: &recordingProc{version: 0}, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	node.Sub(in)
	out := node.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); is.NoErr(node.Run(ctx)) }()

	// Consumer: collect the positions in the order they arrive.
	got := make([]int, 0, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			m := <-out
			p, _ := strconv.Atoi(string(m.Record.Position))
			got = append(got, p)
		}
	}()

	// Producer: send records 1..n, reconfiguring at a deterministic mix of points
	// so many boundaries are exercised without a random seed.
	for i := 1; i <= n; i++ {
		in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte(fmt.Sprintf("%06d", i)), Metadata: map[string]string{}}}
		if i%17 == 0 || i%23 == 0 {
			is.NoErr(node.Reconfigure(ctx, &recordingProc{version: i}))
		}
	}
	close(in)
	<-done
	wg.Wait()

	// Every record delivered exactly once, in order — no drop, no reorder.
	is.Equal(len(got), n)
	for i, p := range got {
		is.Equal(p, i+1)
	}
}

// TestProcessorNode_Reconfigure_ContextCancelledMidSwap is the targeted mid-swap
// fault test (the design doc's PR1 gate for the new crash surface): cancelling the
// node context while a swap is mid-Open must not lose the already-processed record,
// must fail the reconfigure cleanly (old processor kept), and must let the node
// shut down without panic or deadlock.
func TestProcessorNode_Reconfigure_ContextCancelledMidSwap(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())

	old := &recordingProc{version: 0}
	node := &ProcessorNode{Name: "t", Processor: old, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	node.Sub(in)
	out := node.Pub()

	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); runErr = node.Run(ctx) }()

	// One record flows through the old processor.
	in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte("1"), Metadata: map[string]string{}}}
	m := <-out
	is.Equal(m.Record.Metadata["version"], "0")

	// Start a swap whose Open blocks; wait until it is genuinely mid-Open, then
	// cancel the node context.
	newProc := &recordingProc{version: 1, reachedOpen: make(chan struct{}), blockOpen: true}
	reconfErr := make(chan error, 1)
	go func() { reconfErr <- node.Reconfigure(ctx, newProc) }()

	select {
	case <-newProc.reachedOpen:
	case <-time.After(2 * time.Second):
		t.Fatal("swap never reached Open")
	}
	cancel() // cancel mid-swap

	is.True(<-reconfErr != nil) // the swap did not complete
	wg.Wait()
	is.True(runErr != nil) // node shut down on the cancelled context (no panic/deadlock)
}
