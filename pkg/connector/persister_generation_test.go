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

package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// TestPersister_ConcurrentWaitAndPersist_DoesNotPanic reproduces the production
// crash: connector.Service shares ONE Persister across every connector, and
// Source.Teardown calls WaitPendingWritesContext — so any pipeline where one
// source tears down while another is still acking put a Wait and an Add on the
// same sync.WaitGroup concurrently.
//
// That is the documented WaitGroup reuse hazard, and it does not merely race:
//
//	panic: sync: WaitGroup is reused before previous Wait has returned
//
// which killed the process. It reproduced in a shipped binary on ordinary
// shutdown of a 2-source pipeline — 3/3 runs under arch-v2 (one Worker per
// source, so all tear down at once), 1/3 under v1.
//
// Without the per-generation fix this panics in well under a second; the
// panic is a process-level abort, so it fails the test binary outright rather
// than being recoverable.
func TestPersister_ConcurrentWaitAndPersist_DoesNotPanic(t *testing.T) {
	db := &inmemory.DB{}
	// bundleCountThreshold 1: every Persist triggers a flush, maximizing the
	// rate at which new generations are created against concurrent waiters.
	p := NewPersister(log.Nop(), db, DefaultPersisterDelayThreshold, 1)

	conn := &Instance{ID: "conn-b", persister: p}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Connector B keeps persisting — the "Add" side.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = p.Persist(context.Background(), conn, func(error) {})
			}
		}()
	}

	// Connector A keeps doing what Source.Teardown does — the "Wait" side.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p.WaitPendingWrites()
			}
		}()
	}

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()
}

// TestPersister_WaitPendingWrites_WaitsForCallbacks pins the half of the
// contract that is easy to lose when swapping the WaitGroups out: the wait must
// cover the PersistCallback side effects, not just the store write. Approach A's
// deferred plugin-ack (see connector.Source.Ack) depends on it.
func TestPersister_WaitPendingWrites_WaitsForCallbacks(t *testing.T) {
	db := &inmemory.DB{}
	p := NewPersister(log.Nop(), db, DefaultPersisterDelayThreshold, 1)
	conn := &Instance{ID: "conn-a", persister: p}

	var callbackDone bool
	var mu sync.Mutex

	err := p.Persist(context.Background(), conn, func(error) {
		time.Sleep(50 * time.Millisecond) // callback with real work in it
		mu.Lock()
		callbackDone = true
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	p.WaitPendingWrites()

	mu.Lock()
	defer mu.Unlock()
	if !callbackDone {
		t.Fatal("WaitPendingWrites returned before the PersistCallback finished")
	}
}
