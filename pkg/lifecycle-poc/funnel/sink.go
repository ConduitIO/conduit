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

	"github.com/conduitio/conduit-commons/rollback"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Sink owns the destination-side portion of an N-source pipeline's task
// graph - the pipeline-level (shared) processors and the destination
// branch(es) - that every source Worker's own task chain converges on. It
// exists because of a structural conflict introduced by slice 3b of the
// arch-v2 multi-connector epic (N sources, one worker per source, one shared
// destination):
//
//   - Worker.Open/Close must run exactly once per PHYSICAL connector. Calling
//     connector.Destination.Open twice on the same instance is a hard error
//     ("another instance of the connector is already running"), and calling
//     Teardown while a sibling worker is still mid-Write is a data-loss bug
//     (a still-running source's batch would be writing to a destination that
//     no longer exists).
//   - Worker.Do's runtime traversal (doTask/doNextTask) still needs to reach
//     the SAME destination TaskNode instances from every source worker's own
//     goroutine, because that's what lets each worker's own Ack/Nack calls
//     keep routing back to ITS OWN Source (see the package doc's "isolate by
//     construction" note) even though the write itself is physically shared.
//
// Sink resolves this split: its root(s) are built exactly once (see
// lifecycle-poc.Service.buildSharedTail) and marked via
// TaskNode.MarkSharedBoundary, which is what makes a Worker's own Open/Close
// walk stop before touching them (see the sharedBoundary field doc) while
// runtime execution (which never uses that iterator) passes through
// unaffected. Sink.Open is called exactly once, before any worker starts;
// Sink.Close is called exactly once, only after every worker has exited
// (workersWg.Wait() in lifecycle-poc.Service.runPipeline) - closing it any
// earlier would tear down a destination other still-running workers are
// still writing to. Concurrent entry from multiple workers into the shared
// subtree at runtime is serialized by the sharedMu lock TaskNode carries
// (see MarkSharedBoundary and the acquisition site in Worker.doTask) - not by
// anything in this type, which only owns the lifecycle (Open/Close), never
// the per-batch execution.
//
// A pipeline's DLQ is deliberately NOT part of Sink: slice 3b gives every
// source its own DLQ connector (see buildDLQ's per-source naming), so DLQ
// Open/Close stays exactly where it already was - inside Worker.Open/Close,
// via the w.DLQ field, entirely outside the TaskNode graph Sink walks.
type Sink struct {
	// roots holds one or more independent shared subtrees. Normally exactly
	// one (the shared pipeline-processor chain, whose own tail already fans
	// out internally to every destination branch - see buildSharedTail). It
	// can hold more than one only when there are no shared pipeline-level
	// processors AND more than one destination branch: with no single shared
	// node to serve as one fan-out parent, every destination branch is its
	// own independent shared root, each with its own lock - see
	// buildSharedTail's doc for why that is not just safe but preferable
	// (different destinations can then proceed without blocking each other).
	roots []*TaskNode
}

// NewSink builds a Sink over the given shared subtree root(s) and marks each
// one via TaskNode.MarkSharedBoundary. Every root must be the true entry
// point of its subtree - see MarkSharedBoundary's doc on why marking
// anything else is a caller bug.
func NewSink(roots ...*TaskNode) (*Sink, error) {
	if len(roots) == 0 {
		return nil, cerrors.New("(bug) sink has no shared roots")
	}
	seen := make(map[string]bool)
	for _, root := range roots {
		for t := range root.Tasks() {
			if seen[t.ID()] {
				return nil, cerrors.Errorf("task %s included multiple times in the shared sink", t.ID())
			}
			seen[t.ID()] = true
		}
		root.MarkSharedBoundary()
	}
	return &Sink{roots: roots}, nil
}

// Open opens every task reachable from every root, exactly once. If any task
// fails to open, every task already opened by this call is rolled back
// (Closed) before the error is returned - mirroring Worker.Open's own
// all-or-nothing behavior.
func (s *Sink) Open(ctx context.Context) (err error) {
	var r rollback.R
	defer func() {
		rollbackErr := r.Execute()
		err = cerrors.LogOrReplace(err, rollbackErr, func() {})
	}()

	for _, root := range s.roots {
		for task := range root.Tasks() {
			err = task.Open(ctx)
			if err != nil {
				return cerrors.Errorf("task %s failed to open: %w", task.ID(), err)
			}
			r.Append(func() error {
				return task.Close(ctx)
			})
		}
	}

	r.Skip()
	return nil
}

// Close closes every task reachable from every root, exactly once.
//
// Invariant 1/3 (enforcement site): this must only ever be called after
// EVERY source Worker sharing this Sink has exited - see runPipeline's
// workersWg.Wait() call, which gates this. Closing it any earlier would tear
// down a destination a still-running source's Worker is still writing to,
// silently dropping whatever batch is in flight (the write would fail
// against a torn-down plugin, and - because the Worker never gets to Ack -
// invariant 3 is preserved: at worst the record replays on the next attempt,
// but the pipeline would spuriously error every remaining source out from
// under the operator, which is why this ordering is enforced by the caller
// and asserted by tests, not just documented here).
func (s *Sink) Close(ctx context.Context) error {
	var errs []error
	for _, root := range s.roots {
		for task := range root.Tasks() {
			if err := task.Close(ctx); err != nil {
				errs = append(errs, cerrors.Errorf("task %s failed to close: %w", task.ID(), err))
			}
		}
	}
	return cerrors.Join(errs...)
}
