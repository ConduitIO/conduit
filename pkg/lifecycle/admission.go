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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// Admission by run liveness (#2899 item 2; design doc
// 20261007-lifecycle-status-write-failure, rule R3).
//
// Start, Stop, the recovery restart, and Delete/Update (through IsActive)
// decide whether a pipeline has a run by asking the registry, under
// publishMu, instead of reading the pipeline's status. The status is a report
// written after the fact; it lagged or contradicted the run in several
// windows, which let two runs read one source (duplicate delivery, positions
// moving backwards) and let a live run be refused by Stop.
//
// A pipeline ID is in one of these states:
//
//   - free: no reservation and no registered run;
//   - finishing: the registered run's nodes are dead and its cleanup is
//     classifying it or writing its terminal tail;
//   - starting: a Start holds a reservation while it builds and starts the run;
//   - live: the run is published in runningPipelines;
//   - backoff: the published run's nodes are dead and its cleanup is waiting out
//     a recovery delay before it restarts the pipeline.
//
// Invariant 2: at most one run per pipeline ID holds a reservation or a live
// entry. Start is granted only on free or backoff; on backoff it supersedes
// the pending restart, which is then abandoned. On finishing it is refused
// with pipeline.stopping (retryable): the finishing run's terminal status
// write would otherwise race the new run's Running. The recovery restart reserves
// the same way but is granted only if its run is still the registered one and
// has not been superseded, so exactly one of an external Start and the
// pending restart wins.

// runPhase is where a registered run is in its life. Guarded by publishMu.
type runPhase int

const (
	// phaseLive: published; nodes may be running. The zero value, because a
	// run enters runningPipelines only when it is published.
	phaseLive runPhase = iota
	// phaseBackoff: nodes dead; the cleanup goroutine waits out a recovery
	// delay before restarting the pipeline.
	phaseBackoff
	// phaseFinishing: nodes dead; the cleanup goroutine is classifying the run
	// or running its terminal tail. A Start is granted.
	phaseFinishing
)

// reservation is held by a Start from admission until its run is published
// (or the start fails). Guarded by publishMu, except done.
type reservation struct {
	// stop is a Stop that arrived while the run was being built. The run
	// applies it as soon as it is published.
	stop *pendingStop
	// done is closed when the reservation is consumed by the publication or
	// released, so WaitPipeline can wait for a starting run.
	done chan struct{}
	// predecessor is the run a recovery restart restarts, nil for a Start.
	predecessor *runnablePipeline
}

type pendingStop struct {
	force bool
}

// errRecoverySuperseded is returned by StartWithBackoff when a Start took
// over the pipeline while the restart was pending. The cleanup goroutine then
// leaves the pipeline to the new run: no status, no terminal error, no
// notification.
var errRecoverySuperseded = cerrors.New("recovery superseded: the pipeline was started again")

func errPipelineStopping(pipelineID string) error {
	err := conduiterr.Wrap(
		pipeline.CodePipelineStopping,
		fmt.Sprintf("can't start pipeline %s: %s", pipelineID, pipeline.ErrPipelineStopping),
		pipeline.ErrPipelineStopping,
	)
	err.Suggestion = "the pipeline's previous run is still finishing; retry the start in a moment"
	return err
}

func errPipelineRunning(pipelineID string) error {
	// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
	// wrapped, ConduitError adds the code.
	err := conduiterr.Wrap(
		pipeline.CodePipelineRunning,
		fmt.Sprintf("can't start pipeline %s: %s", pipelineID, pipeline.ErrPipelineRunning),
		pipeline.ErrPipelineRunning,
	)
	err.Suggestion = "the pipeline is already running or starting; stop it first if you need to restart it"
	return err
}

// reserve admits a start of pipelineID. predecessor is nil for an external
// Start and the backoff run for a recovery restart. It returns
// errPipelineRunning if a run is starting or live, and errRecoverySuperseded
// if predecessor is no longer the pipeline's registered, unsuperseded run.
func (s *Service) reserve(pipelineID string, predecessor *runnablePipeline) (*reservation, error) {
	if s.testAtReservation != nil {
		s.testAtReservation(pipelineID, predecessor)
	}

	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	// Invariant 2: at most one run per pipeline ID holds a reservation or a
	// live entry.
	if _, ok := s.starting[pipelineID]; ok {
		if predecessor != nil {
			return nil, errRecoverySuperseded
		}
		return nil, errPipelineRunning(pipelineID)
	}
	cur, ok := s.runningPipelines.Get(pipelineID)
	switch {
	case predecessor != nil:
		if !ok || cur != predecessor || predecessor.superseded {
			return nil, errRecoverySuperseded
		}
		// Invariant 7: a run someone asked to stop is never restarted. Stop
		// records the request under this lock.
		if err := predecessorStopErr(predecessor); err != nil {
			return nil, err
		}
	case ok && cur.phase == phaseLive:
		return nil, errPipelineRunning(pipelineID)
	case ok && cur.phase == phaseFinishing:
		// The run's nodes are dead and its terminal tail has not finished
		// writing. A new run now could have its Running overwritten by that
		// tail, so refuse until it is done (retryable).
		return nil, errPipelineStopping(pipelineID)
	case ok:
		// backoff: this Start takes over. The pending restart is abandoned
		// (it checks superseded under this lock).
		cur.supersede()
	}

	res := &reservation{done: make(chan struct{}), predecessor: predecessor}
	if s.starting == nil {
		s.starting = make(map[string]*reservation)
	}
	s.starting[pipelineID] = res
	return res, nil
}

// releaseReservation drops res if the start it admitted did not publish a
// run. It is a no-op once the publication consumed res.
func (s *Service) releaseReservation(pipelineID string, res *reservation) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if s.starting[pipelineID] == res {
		delete(s.starting, pipelineID)
		close(res.done)
	}
}

// consumeReservationLocked hands rp's reservation over to the published
// entry and returns a Stop that arrived while rp was being built. publishMu
// must be held.
func (s *Service) consumeReservationLocked(rp *runnablePipeline) *pendingStop {
	res := rp.reservation
	if res == nil || s.starting[rp.pipeline.ID] != res {
		return nil
	}
	delete(s.starting, rp.pipeline.ID)
	close(res.done)
	return res.stop
}

// setPhase moves rp to phase. Moving to backoff fails if rp was superseded
// (a Start took over while rp was finishing), in which case rp must not
// restart the pipeline.
func (s *Service) setPhase(rp *runnablePipeline, phase runPhase) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if phase == phaseBackoff && rp.superseded {
		return false
	}
	rp.phase = phase
	return true
}

// IsActive reports whether pipelineID has a run that is starting, live, or
// waiting out a recovery backoff. The orchestrator refuses to delete or
// update such a pipeline. The pipeline's status is not consulted.
func (s *Service) IsActive(pipelineID string) bool {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if _, ok := s.starting[pipelineID]; ok {
		return true
	}
	_, ok := s.runningPipelines.Get(pipelineID)
	// A finishing run counts: it may still move to a recovery backoff.
	return ok
}
