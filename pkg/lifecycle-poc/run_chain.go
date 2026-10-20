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

import "gopkg.in/tomb.v2"

// runChain is the tomb of a published run and of every earlier run of the
// same pipeline that was still alive when it was published (#3087).
//
// A recovery restart is published from the failed run's cleanup goroutine,
// which keeps running after the publication: it finishes announcing the
// restart, logs, and releases its reservation, and only then returns. So the
// restarted run can be stopped and dead while its predecessor is still
// running. WaitPipeline waits for the whole chain, not only for the run it
// finds, so that it never reports a pipeline stopped while any run of it is
// still running.
//
// A chain only links runs that were not dead at publication, so it stays short:
// dead links are dropped every time a run is published.
type runChain struct {
	t    *tomb.Tomb
	prev *runChain
}

// newRunChain links t in front of the part of prev that is not dead yet.
// A nil t (a run without a tomb, which only tests publish) adds no link.
func newRunChain(t *tomb.Tomb, prev *runChain) *runChain {
	if t == nil {
		return prev.alive()
	}
	return &runChain{t: t, prev: prev.alive()}
}

// alive returns the chain without its dead links (see isDead). It copies the links it
// keeps, so a chain another run holds is never changed.
func (c *runChain) alive() *runChain {
	if c == nil {
		return nil
	}
	rest := c.prev.alive()
	if isDead(c.t) {
		return rest
	}
	if rest == c.prev {
		return c
	}
	return &runChain{t: c.t, prev: rest}
}

// isDead reports whether every goroutine of t has returned. Not t.Alive():
// that is false as soon as the tomb is dying, and a recovery predecessor is
// dying (its nodes failed) while its cleanup goroutine still runs.
func isDead(t *tomb.Tomb) bool {
	select {
	case <-t.Dead():
		return true
	default:
		return false
	}
}

// wait blocks until every tomb in the chain is dead.
func (c *runChain) wait() {
	for ; c != nil; c = c.prev {
		<-c.t.Dead()
	}
}
