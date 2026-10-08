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

package lifecycle

import (
	"context"
	"fmt"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// Per-run fencing of status, terminal error and notifications (#2899 item 3;
// design doc 20261007-lifecycle-status-write-failure, rule R4).
//
// A run's cleanup writes the pipeline's status, its terminal error and the
// OnFailure notification by pipeline ID. When a newer run has replaced it, those
// writes land on a pipeline the newer run owns: its status is overwritten,
// WaitPipeline returns the old run's error, and exit-on-degraded can fire for an
// error that no longer describes the pipeline.
//
// The rules:
//
//   - A run writes the pipeline's status only while it is the pipeline's
//     registered run (runningPipelines[id] == rp). The check and the write
//     happen under the pipeline's status lock, and a newer run's first write
//     (its StatusRunning) takes the same lock, so a write the old run has
//     already decided on lands before the newer run's, never after.
//   - A run records its terminal error and notifies OnFailure only while it
//     owns the pipeline: registered and not superseded by a Start. A
//     superseded run's failure never trips exit-on-degraded (approved
//     decision 5).
//   - A Start that takes over a run in recovery and then fails to start
//     leaves the pipeline with no run. Whichever of the two finishes last,
//     the failed Start or the superseded run's cleanup, records the outcome,
//     and the outcome does not depend on which: the superseded run's own
//     terminal decision if it made one (as if it had never been superseded),
//     otherwise SystemStopped on shutdown, UserStopped if a stop was
//     requested, Degraded with the start error otherwise. Without that, the
//     status kept saying Recovering with nothing running. (A Start is
//     refused while a run is finishing, so only a run in recovery backoff is
//     ever superseded.)
//
// The design doc places the token on pipeline.Instance and fences in
// pipeline.Service. The lifecycle is the only writer of run statuses, so the
// same guarantee is enforced here, at the writer, with the run's registry entry
// as the token; nothing in pipeline.Service or the persisted instance changes.

// lockStatus takes the lock that orders status writes for pipelineID and
// returns its unlock. The lock is reference-counted: its map entry is removed
// when the last holder or waiter releases it, so deleted pipelines leave
// nothing behind.
func (s *Service) lockStatus(pipelineID string) (unlock func()) {
	s.statusLocksMu.Lock()
	if s.statusLocks == nil {
		s.statusLocks = make(map[string]*statusLock)
	}
	l, ok := s.statusLocks[pipelineID]
	if !ok {
		l = &statusLock{}
		s.statusLocks[pipelineID] = l
	}
	l.refs++
	s.statusLocksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.statusLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.statusLocks, pipelineID)
		}
		s.statusLocksMu.Unlock()
	}
}

// statusLock is one pipeline's status lock with its reference count, which
// statusLocksMu guards.
type statusLock struct {
	mu   sync.Mutex
	refs int
}

// isRegistered reports whether rp is its pipeline's registered run.
func (s *Service) isRegistered(rp *runnablePipeline) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	cur, ok := s.runningPipelines.Get(rp.pipeline.ID)
	return ok && cur == rp
}

// ownsPipeline reports whether rp is its pipeline's registered run and has
// not been superseded by a Start, or the Start that superseded it has already
// failed (rp then has the last word on the pipeline).
func (s *Service) ownsPipeline(rp *runnablePipeline) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	cur, ok := s.runningPipelines.Get(rp.pipeline.ID)
	return ok && cur == rp && (!rp.superseded || rp.takeoverErr != nil)
}

// writeRunStatus writes status for rp if rp is still its pipeline's
// registered run, and reports whether it did. The write is bounded by ctx.
func (s *Service) writeRunStatus(ctx context.Context, rp *runnablePipeline, status pipeline.Status, errMsg string) (bool, error) {
	if s.testBeforeStatusWrite != nil {
		s.testBeforeStatusWrite(rp, status)
	}
	unlock := s.lockStatus(rp.pipeline.ID)
	defer unlock()
	// Invariant 7 / R4: a run writes its pipeline's status only while it is
	// the registered run; a newer run's writes are never overwritten.
	if !s.isRegistered(rp) {
		s.logger.Debug(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Any(log.PipelineStatusField, status).
			Msg("dropping the status write of a run that no longer owns the pipeline")
		return false, nil
	}
	return true, s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, status, errMsg)
}

// takeoverOutcome is how a superseded run ended. It is kept on the run so
// that, if the Start that superseded it fails, the pipeline's outcome is
// recorded the same way whichever of the two finished first.
type takeoverOutcome struct {
	// abandoned: the run left its recovery to the Start and decided
	// nothing. The outcome then depends on why the Start failed.
	abandoned bool
	// runErr is the error the run failed with.
	runErr error
	// status, msg, termErr and notify are the terminal decision the run made
	// (when not abandoned): what it wrote, what WaitPipeline should return,
	// and whether it is a failure for OnFailure.
	status  pipeline.Status
	msg     string
	termErr error
	notify  bool
}

// endSuperseded is called when a run that a Start superseded finishes its
// cleanup without owning the pipeline. It keeps how the run ended and
// returns the Start's error if that Start has already failed, in which case
// the caller records the outcome (recordTakeoverOutcome); otherwise the
// Start does, if it fails later.
func (s *Service) endSuperseded(rp *runnablePipeline, out takeoverOutcome) error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	rp.ended = &out
	return rp.takeoverErr
}

// takeoverFailed is called by a Start that superseded took and then failed
// to publish a run. It returns how took ended if its cleanup has already
// finished, in which case the caller records the outcome; otherwise took's
// cleanup does, now that it owns the pipeline again (ownsPipeline).
func (s *Service) takeoverFailed(took *runnablePipeline, err error) *takeoverOutcome {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	took.takeoverErr = err
	return took.ended
}

// recordTakeoverOutcome records the outcome of a pipeline left without a run
// by a failed takeover. If the superseded run had made a terminal decision,
// that decision stands, as if the run had never been superseded. If it had
// abandoned its recovery to the Start:
//
//   - shutdown (the Start was refused, or Conduit is stopping): SystemStopped
//     with the run's error, so the pipeline starts again on the next boot;
//   - a stop was requested: UserStopped with the run's error;
//   - otherwise: Degraded with the Start's error.
//
// Neither of the abandoned outcomes notifies OnFailure: the failure is the
// Start's, and the Start returned it. It does nothing if another Start or run
// has the pipeline by now.
func (s *Service) recordTakeoverOutcome(ctx context.Context, pipelineID string, took *runnablePipeline, out *takeoverOutcome, startErr error) {
	unlock := s.lockStatus(pipelineID)
	defer unlock()

	free := func() bool {
		_, starting := s.starting[pipelineID]
		cur, ok := s.runningPipelines.Get(pipelineID)
		return !starting && (!ok || cur == took)
	}
	s.publishMu.Lock()
	ok := free()
	s.publishMu.Unlock()
	if !ok {
		return
	}

	o := *out
	if o.abandoned {
		switch {
		case cerrors.Is(startErr, pipeline.ErrShuttingDown) || s.isShuttingDown():
			o.status, o.msg, o.termErr = pipeline.StatusSystemStopped, fmt.Sprintf("%+v", o.runErr), o.runErr
		case runStopRequested(took):
			o.status, o.msg, o.termErr = pipeline.StatusUserStopped, fmt.Sprintf("%+v", o.runErr), o.runErr
		default:
			o.status = pipeline.StatusDegraded
			o.msg = fmt.Sprintf("could not start the pipeline while it was recovering: %+v", startErr)
			o.termErr = startErr
		}
		o.notify = false
	}

	s.logger.Err(ctx, startErr).
		Str(log.PipelineIDField, pipelineID).
		Any(log.PipelineStatusField, o.status).
		Msg("pipeline could not be started while it was recovering; it is not running")
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.runningWriteTimeout())
	defer cancel()
	if err := s.pipelines.UpdateStatus(wctx, pipelineID, o.status, o.msg); err != nil {
		s.logStatusNotPersisted(ctx, pipelineID, o.status, err)
	}

	// Fenced like the other per-run writes: a Start that reserves from here
	// on clears the terminal error after it reserves, so it cannot be left
	// with this one.
	s.publishMu.Lock()
	ok = free()
	if ok {
		s.terminalErrors.Set(pipelineID, o.termErr)
	}
	s.publishMu.Unlock()
	if ok && o.notify {
		s.notify(pipelineID, o.termErr)
	}
}
