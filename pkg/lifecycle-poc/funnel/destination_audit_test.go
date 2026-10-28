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

package funnel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// TestClose_DuringDrainDoesNotPanic: a Stop-triggered Drain blocked in its
// hand-off (flush queue full) while Close runs concurrently (Do returned on a
// tomb kill while Stop is still draining). Close used to close flushCh under
// the blocked send, which panics the whole process ("send on closed channel").
func TestClose_DuringDrainDoesNotPanic(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	task.SetPipelined()
	if err := task.Open(runCtx); err != nil {
		t.Fatal(err)
	}
	recs := randomRecords(8)
	alog := &eventLog{}
	blocked, unblock := dest.blockWrites(recs[0].Position)
	for i := 0; i < 3; i++ {
		if err := task.doBuffered(runCtx, NewBatch(recs[2*i:2*i+2]), newRecordingAckNacker(alog)); !isBuffered(err) {
			t.Fatalf("doBuffered %d: %v", i, err)
		}
		if i == 0 {
			<-blocked
		}
	}
	// one more batch buffered (below threshold) so Drain has something to take
	_ = task.doBuffered(runCtx, NewBatch(recs[6:7]), newRecordingAckNacker(alog))
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_ = task.Drain(context.Background()) // blocks in the send: channel full
	}()
	time.Sleep(30 * time.Millisecond)
	closeDone := make(chan struct{})
	go func() { _ = task.Close(context.Background()); close(closeDone) }()
	time.Sleep(30 * time.Millisecond)
	unblock()
	select {
	case p := <-panicked:
		if p != nil {
			t.Fatalf("Drain panicked: %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Drain did not return")
	}
	<-closeDone
}

// TestDrain_HonoursCtxWhileWaitingForEnqueueMu: a sibling's deferOrdered blocked
// in enqueueFlush (full queue behind a wedged flusher) holds enqueueMu. Drain
// must still honour the stop deadline instead of waiting on the mutex, and since
// it took nothing there is nothing to undo and no failure to record.
func TestDrain_HonoursCtxWhileWaitingForEnqueueMu(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	dest := newFakeDestination("d")
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, 2, 0)
	task.SetPipelined()
	if err := task.Open(runCtx); err != nil {
		t.Fatal(err)
	}
	recs := randomRecords(6)
	alog := &eventLog{}
	blocked, release := dest.blockWrites(recs[0].Position)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(release) }
	// The flusher and the sibling goroutine log through t: wait for them.
	sibling := make(chan struct{})
	t.Cleanup(func() {
		unblock()
		cancelRun()
		<-task.flusherDone
		<-sibling
	})
	for i := 0; i < 3; i++ {
		if err := task.doBuffered(runCtx, NewBatch(recs[2*i:2*i+2]), newRecordingAckNacker(alog)); !isBuffered(err) {
			t.Fatalf("doBuffered %d: %v", i, err)
		}
		if i == 0 {
			<-blocked
		}
	}
	go func() { // sibling source's filtered batch: blocks in enqueueFlush holding enqueueMu
		defer close(sibling)
		_ = task.deferOrdered(runCtx, func(context.Context) error { return nil })
	}()
	time.Sleep(30 * time.Millisecond)
	dctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- task.Drain(dctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Drain returned nil although its ctx expired before it could do anything")
		}
		if task.flushError() != nil {
			t.Fatalf("Drain took nothing, so it must not fail the task: %v", task.flushError())
		}
	case <-time.After(time.Second):
		t.Fatalf("Drain(ctx 50ms) still blocked after 1s (enqueueMu taken without ctx)")
	}
}

// TestDrain_DoesNotTakePendingAfterFlushError: after a flush error the flusher is
// gone or going; Drain must not take pending and send it into a dead channel
// (nothing would count or complete it). The batches stay pending, unacknowledged.
func TestDrain_DoesNotTakePendingAfterFlushError(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	task := NewBatchingDestinationTask("d", newFakeDestination("d"), log.Test(t), NoOpConnectorMetrics{}, 10, 0)
	task.SetPipelined()
	if err := task.Open(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancelRun(); <-task.flusherDone })
	if !isBuffered(task.doBuffered(runCtx, NewBatch(randomRecords(1)), newRecordingAckNacker(&eventLog{}))) {
		t.Fatal("expected buffered")
	}
	task.setFlushErr(context.DeadlineExceeded)

	if err := task.Drain(context.Background()); err == nil {
		t.Fatal("Drain must return the flush error")
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if len(task.pending) != 1 || task.outstanding != 0 {
		t.Fatalf("pending=%d outstanding=%d, want the batch left pending and no slot taken", len(task.pending), task.outstanding)
	}
}
