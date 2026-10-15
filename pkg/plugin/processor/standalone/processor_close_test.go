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

package standalone

import (
	"context"
	"runtime"
	"sync"
	"testing"

	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// newGuestlessProcessor builds a wasmProcessor wired to an in-process fake guest
// instead of a WASM module: it answers every request with an Open response and
// exits (closing moduleStopped) when the processor shuts down, whichever way the
// shutdown is signalled (closed channel, or the request channel being closed).
// That is all executeCommand and closeModule need, so the concurrency between
// them can be hammered cheaply and without a wazero runtime.
func newGuestlessProcessor() *wasmProcessor {
	p := &wasmProcessor{
		logger:           log.Nop(),
		commandRequests:  make(chan *processorv1.CommandRequest),
		commandResponses: make(chan tuple[*processorv1.CommandResponse, error]),
		abandoned:        make(chan struct{}),
		closed:           make(chan struct{}),
		moduleStopped:    make(chan struct{}),
		cancelRun:        func() {},
	}
	go func() {
		defer close(p.moduleStopped)
		for {
			select {
			case req, ok := <-p.commandRequests:
				if !ok {
					return
				}
				_ = req
				resp := &processorv1.CommandResponse{Response: &processorv1.CommandResponse_Open{Open: &processorv1.Open_Response{}}}
				select {
				case p.commandResponses <- tuple[*processorv1.CommandResponse, error]{resp, nil}:
				case <-p.abandoned:
				}
			case <-p.closed:
				return
			}
		}
	}()
	return p
}

// TestWASMProcessor_CommandsConcurrentWithTeardown_NoPanic is the regression
// test for a send-on-closed-channel panic: closeModule closed commandRequests
// while executeCommand, having passed its "already closed?" check, was about to
// send on it (a check-then-act race), so a Process running concurrently with
// Teardown crashed the process. The channel must never be closed; shutdown is
// signalled on the separate closed channel.
func TestWASMProcessor_CommandsConcurrentWithTeardown_NoPanic(t *testing.T) {
	openReq := &processorv1.CommandRequest{Request: &processorv1.CommandRequest_Open{Open: &processorv1.Open_Request{}}}

	for i := 0; i < 500; i++ {
		p := newGuestlessProcessor()

		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if _, err := p.executeCommand(context.Background(), openReq); err != nil {
						return // refused or the module stopped: shutdown observed
					}
				}
			}()
		}

		for j := 0; j < i%7; j++ { // vary where in the stream of commands the close lands
			runtime.Gosched()
		}
		if err := p.closeModule(context.Background()); err != nil {
			t.Fatalf("closeModule: %v", err)
		}
		wg.Wait()
	}
}
