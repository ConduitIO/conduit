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
//     the failed Start or the superseded run's cleanup, records it: status
//     Degraded with the start error, and that error as the terminal error.
//     Without that, the status kept saying Recovering with nothing running.
//     A superseded run that ends through a terminal arm instead (a stop or
//     a shutdown that reached it during the backoff) writes its own status
//     as usual; if the Start that superseded it has already failed, it also
//     records its terminal error and notifies, as if it had never been
//     superseded. (A Start is refused while a run is finishing, so only a
//     run in recovery backoff is ever superseded.)
//
// The design doc places the token on pipeline.Instance and fences in
// pipeline.Service. The lifecycle is the only writer of run statuses, so the
// same guarantee is enforced here, at the writer, with the run's registry entry
// as the token; nothing in pipeline.Service or the persisted instance changes.

// statusLock returns the lock that orders status writes for pipelineID.
func (s *Service) statusLock(pipelineID string) *sync.Mutex {
	s.statusLocksMu.Lock()
	defer s.statusLocksMu.Unlock()
	if s.statusLocks == nil {
		s.statusLocks = make(map[string]*sync.Mutex)
	}
	l, ok := s.statusLocks[pipelineID]
	if !ok {
		l = &sync.Mutex{}
		s.statusLocks[pipelineID] = l
	}
	return l
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
	l := s.statusLock(rp.pipeline.ID)
	l.Lock()
	defer l.Unlock()
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

// handOver is called by a superseded run's cleanup when it abandons its
// recovery and leaves the pipeline to the Start that took it over. It returns
// the takeover's error if that Start already failed, in which case this run
// records the outcome (finishFailedTakeover); otherwise the Start will, if it
// fails later. A superseded run that ends through a terminal arm instead
// writes its own terminal status and does not hand over.
func (s *Service) handOver(rp *runnablePipeline) error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	rp.abandoned = true
	return rp.takeoverErr
}

// takeoverFailed is called by a Start that superseded took and then failed
// to publish a run. It reports whether took's cleanup already abandoned its
// recovery to this Start, in which case the caller records the outcome;
// otherwise took's cleanup does (handOver, or its own terminal arm).
func (s *Service) takeoverFailed(took *runnablePipeline, err error) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	took.takeoverErr = err
	return took.abandoned
}

// finishFailedTakeover records a pipeline left without a run by a failed
// takeover: status Degraded with startErr, and startErr as the terminal error.
// It does nothing if another Start or run has the pipeline by now. It does not
// notify OnFailure: the failure is the Start's, and the Start returned it.
func (s *Service) finishFailedTakeover(ctx context.Context, pipelineID string, took *runnablePipeline, startErr error) {
	l := s.statusLock(pipelineID)
	l.Lock()
	defer l.Unlock()

	s.publishMu.Lock()
	_, starting := s.starting[pipelineID]
	cur, ok := s.runningPipelines.Get(pipelineID)
	s.publishMu.Unlock()
	if starting || (ok && cur != took) {
		return
	}

	s.logger.Err(ctx, startErr).
		Str(log.PipelineIDField, pipelineID).
		Msg("pipeline could not be started while it was recovering; it is not running")
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.runningWriteTimeout())
	defer cancel()
	msg := fmt.Sprintf("could not start the pipeline while it was recovering: %+v", startErr)
	if err := s.pipelines.UpdateStatus(wctx, pipelineID, pipeline.StatusDegraded, msg); err != nil {
		s.logStatusNotPersisted(ctx, pipelineID, pipeline.StatusDegraded, err)
	}
	s.terminalErrors.Set(pipelineID, startErr)
}
