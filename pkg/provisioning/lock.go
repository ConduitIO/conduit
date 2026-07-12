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

package provisioning

import "sync"

// pipelineLocks is a keyed mutex, one lock per pipeline ID. ApplyPlan and
// ApplyPlanLive each hold the target pipeline's lock for their *entire* body
// (recompute, hash check, running check, stop/import/start) so two concurrent
// Apply*/ApplyPlanLive calls against the SAME pipeline ID can never
// interleave — this is the Tier-1 hard gate called for by the design doc's
// "Concurrent applies / apply-during-manual-stop" failure mode and
// ApplyPlanLive's former KNOWN GAP comment (PR1 of #2588); closed here in
// PR2. Calls for two *different* pipeline IDs never block each other.
//
// The lock is scoped to this Service (in-process). It says nothing about a
// second `conduit run` process pointed at the same store — that hazard is
// already ruled out by NewLocalService's Badger-only default-deny (see its
// doc) for the standalone CLI path, and does not apply to the live-server
// path this lock protects (there is exactly one provisioning.Service, and
// therefore exactly one lifecycle, per running server).
type pipelineLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newPipelineLocks() *pipelineLocks {
	return &pipelineLocks{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until the caller holds the named pipeline's lock, then returns
// a func that releases it. Safe for concurrent use by multiple goroutines,
// including concurrent Lock calls for the same or different ids.
//
// Locks are created lazily and never removed. A conduit process applies to a
// small, effectively bounded set of pipeline IDs over its lifetime (bounded
// by how many pipelines a human/agent ever defines against it), so the
// map's unbounded growth is a deliberate, safe trade-off: reclaiming entries
// (e.g. refcounting, or deleting once a waiter count hits zero) risks freeing
// a *sync.Mutex out from under a concurrent Lock call that already looked it
// up but hasn't locked it yet — a correctness bug in exchange for memory this
// workload doesn't need to reclaim.
func (p *pipelineLocks) Lock(id string) func() {
	p.mu.Lock()
	l, ok := p.locks[id]
	if !ok {
		l = &sync.Mutex{}
		p.locks[id] = l
	}
	p.mu.Unlock()

	l.Lock()
	return l.Unlock
}
