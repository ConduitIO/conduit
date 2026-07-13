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
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// tagProcessor returns a mock Processor whose Process tags each record's metadata
// with by=<tag>, so a test can prove which processor handled a given record.
func tagProcessor(ctrl *gomock.Controller, tag string) *mock.Processor {
	p := mock.NewProcessor(ctrl)
	p.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got []opencdc.Record) []sdk.ProcessedRecord {
			got[0].Metadata["by"] = tag
			return []sdk.ProcessedRecord{sdk.SingleRecord(got[0])}
		}).AnyTimes()
	return p
}

// TestProcessorNode_Reconfigure_SwapAtRecordBoundary proves the core in-place
// swap: record N goes through the old processor, and after Reconfigure returns,
// record N+1 goes through the new one — the source never restarts, and the swap
// happens at a clean record boundary.
func TestProcessorNode_Reconfigure_SwapAtRecordBoundary(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	procA := tagProcessor(ctrl, "A")
	procA.EXPECT().Open(gomock.Any())
	procA.EXPECT().Teardown(gomock.Any()) // torn down when swapped out

	procB := tagProcessor(ctrl, "B")
	procB.EXPECT().Open(gomock.Any())     // opened during the swap
	procB.EXPECT().Teardown(gomock.Any()) // torn down when the node stops

	n := &ProcessorNode{Name: "test", Processor: procA, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		is.NoErr(n.Run(ctx))
	}()

	// record 1 -> processor A
	in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte("p1"), Metadata: map[string]string{}}}
	got1 := <-out
	is.Equal(got1.Record.Metadata["by"], "A")

	// swap to B; Reconfigure blocks until the Run goroutine applies it
	is.NoErr(n.Reconfigure(ctx, procB))

	// record 2 -> processor B
	in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte("p2"), Metadata: map[string]string{}}}
	got2 := <-out
	is.Equal(got2.Record.Metadata["by"], "B")

	close(in)
	wg.Wait()
	_, ok := <-out
	is.Equal(false, ok) // out closed on stop
}

// TestProcessorNode_Reconfigure_OpenFailureKeepsOldProcessor proves a bad edit
// never drops the pipeline: if the new processor fails to Open, the old one keeps
// running and the error is returned to the caller (open-before-teardown).
func TestProcessorNode_Reconfigure_OpenFailureKeepsOldProcessor(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	procA := tagProcessor(ctrl, "A")
	procA.EXPECT().Open(gomock.Any())
	procA.EXPECT().Teardown(gomock.Any()) // kept through the failed swap, torn down on stop

	openErr := cerrors.New("bad new config")
	procB := mock.NewProcessor(ctrl)
	procB.EXPECT().Open(gomock.Any()).Return(openErr)
	procB.EXPECT().Teardown(gomock.Any()) // best-effort cleanup of the failed processor

	n := &ProcessorNode{Name: "test", Processor: procA, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		is.NoErr(n.Run(ctx))
	}()

	// idle reconfigure that fails to open -> returns the error, keeps A
	err := n.Reconfigure(ctx, procB)
	is.True(err != nil)
	is.True(cerrors.Is(err, openErr))

	// A is still active: a record must still flow through it
	in <- &Message{Ctx: ctx, Record: opencdc.Record{Position: []byte("p"), Metadata: map[string]string{}}}
	got := <-out
	is.Equal(got.Record.Metadata["by"], "A")

	close(in)
	wg.Wait()
}

// TestProcessorNode_Reconfigure_IdlePipelineAppliesPromptly proves the wake
// signal applies a swap even when no records are flowing (the interruptible-wait
// requirement): with nothing on the inbound channel, Reconfigure must still
// return promptly rather than block until the next record.
func TestProcessorNode_Reconfigure_IdlePipelineAppliesPromptly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	procA := mock.NewProcessor(ctrl)
	procA.EXPECT().Open(gomock.Any())
	procA.EXPECT().Teardown(gomock.Any()) // torn down during the swap

	procB := mock.NewProcessor(ctrl)
	procB.EXPECT().Open(gomock.Any())
	procB.EXPECT().Teardown(gomock.Any()) // torn down on stop

	n := &ProcessorNode{Name: "test", Processor: procA, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		is.NoErr(n.Run(ctx))
	}()

	// no records are sent; the pipeline is idle
	done := make(chan error, 1)
	go func() { done <- n.Reconfigure(ctx, procB) }()
	select {
	case err := <-done:
		is.NoErr(err)
	case <-time.After(2 * time.Second):
		t.Fatal("reconfigure did not apply on an idle pipeline within 2s")
	}

	close(in)
	wg.Wait()
	_, ok := <-out
	is.Equal(false, ok)
}

// TestProcessorNode_Reconfigure_ContextCancelled proves Reconfigure unblocks and
// returns ctx.Err() if its context is cancelled before the swap is applied
// (here the node is not running, so nothing applies it).
func TestProcessorNode_Reconfigure_ContextCancelled(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	// Neither processor is ever opened: Run is not started, and the request is
	// withdrawn on cancel. No expectations => any call would fail the test.
	n := &ProcessorNode{Name: "test", Processor: mock.NewProcessor(ctrl), ProcessorTimer: noop.Timer{}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := n.Reconfigure(ctx, mock.NewProcessor(ctrl))
	is.True(cerrors.Is(err, context.Canceled))
}

// teardownSpy is a Processor that records whether it was torn down via the plain
// Teardown (which, for the real RunnableProcessor, clears the shared Instance's
// running flag) or via TeardownForReconfigure (which does not). It lets the swap
// tests below assert that applyPendingSwap never uses the running-clearing
// Teardown on a processor that is being swapped while its instance stays running.
type teardownSpy struct {
	openErr error

	mu            sync.Mutex
	teardownCalls int
	reconfigCalls int
}

func (s *teardownSpy) Open(context.Context) error { return s.openErr }

func (s *teardownSpy) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(recs))
	for i, r := range recs {
		out[i] = sdk.SingleRecord(r)
	}
	return out
}

func (s *teardownSpy) Teardown(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teardownCalls++
	return nil
}

func (s *teardownSpy) TeardownForReconfigure(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconfigCalls++
	return nil
}

func (s *teardownSpy) counts() (teardown, reconfig int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.teardownCalls, s.reconfigCalls
}

// TestProcessorNode_Reconfigure_SuccessfulSwap_UsesReconfigureTeardown proves the
// swap tears the OLD processor down via TeardownForReconfigure (not the running
// -clearing Teardown), and that a real pipeline STOP still uses the plain Teardown
// on the node's current processor. This is the wiring half of the shared-instance
// running-flag regression (the processor-package half lives in
// TestReconfigureSwap_KeepsInstanceRunning_GuardsStayArmed).
func TestProcessorNode_Reconfigure_SuccessfulSwap_UsesReconfigureTeardown(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	old := &teardownSpy{}
	newp := &teardownSpy{}
	n := &ProcessorNode{Name: "test", Processor: old, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		is.NoErr(n.Run(ctx))
	}()

	is.NoErr(n.Reconfigure(ctx, newp)) // blocks until the swap is applied

	oldTd, oldRc := old.counts()
	is.Equal(oldTd, 0) // the running-clearing Teardown must NOT run on a swap
	is.Equal(oldRc, 1) // the old processor was torn down via TeardownForReconfigure

	close(in)
	wg.Wait()

	newTd, newRc := newp.counts()
	is.Equal(newTd, 1) // a real stop uses the plain, running-clearing Teardown
	is.Equal(newRc, 0)

	_, ok := <-out
	is.Equal(false, ok)
}

// TestProcessorNode_Reconfigure_OpenFailure_UsesReconfigureTeardown proves that
// when the NEW processor fails to open, its best-effort cleanup also uses
// TeardownForReconfigure — the old processor (sharing the instance) keeps running,
// so its running flag must not be cleared by tearing down the failed new one.
func TestProcessorNode_Reconfigure_OpenFailure_UsesReconfigureTeardown(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	old := &teardownSpy{}
	newp := &teardownSpy{openErr: cerrors.New("bad new config")}
	n := &ProcessorNode{Name: "test", Processor: old, ProcessorTimer: noop.Timer{}}
	in := make(chan *Message)
	n.Sub(in)
	out := n.Pub()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		is.NoErr(n.Run(ctx))
	}()

	err := n.Reconfigure(ctx, newp)
	is.True(err != nil) // open failed, swap rejected

	newTd, newRc := newp.counts()
	is.Equal(newTd, 0) // failed-new cleanup must not use the running-clearing Teardown
	is.Equal(newRc, 1) // it used TeardownForReconfigure

	oldTd, oldRc := old.counts()
	is.Equal(oldTd, 0) // old kept running, untouched by the failed swap
	is.Equal(oldRc, 0)

	close(in)
	wg.Wait()

	oldTd2, _ := old.counts()
	is.Equal(oldTd2, 1) // stop tears the still-current old processor down fully

	_, ok := <-out
	is.Equal(false, ok)
}
