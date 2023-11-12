// Copyright © 2022 Meroxa, Inc.
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

package inspector

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
)

const DefaultBufferSize = 1000

// Session represents a single inspector session. Records are continuously sent
// into channel C. If the buffer of C is full, records will be dropped. C will
// be closed once the session is removed from the inspector.
type Session struct {
	C chan record.Record

	id          string
	componentID string
}

// Inspector is attached to an inspectable pipeline component
// and makes returns records coming in or out of the component.
// An Inspector is a "proxy" between the pipeline component being
// inspected and the API, which broadcasts records to all clients.
type Inspector struct {
	// sessions is a map of sessions.
	// keys are sessions IDs.
	sessions map[string]*Session
	// guards access to sessions
	lock sync.Mutex
	// hasSessions is set to true when there are open sessions. This allows us
	// to take a shortcut without acquiring the lock in the happy path, when
	// there are no sessions.
	hasSessions atomic.Bool

	logger     log.CtxLogger
	bufferSize int
}

func New(logger log.CtxLogger, bufferSize int) *Inspector {
	return &Inspector{
		sessions:   make(map[string]*Session),
		logger:     logger.WithComponent("inspector.Inspector"),
		bufferSize: bufferSize,
	}
}

// NewSession creates a new session in given inspector.
// componentID is the ID of the component being inspected (connector or processor).
// The session will be closed and removed from the inspector when the context is
// closed.
func (i *Inspector) NewSession(ctx context.Context, componentID string) *Session {
	s := &Session{
		C:           make(chan record.Record, i.bufferSize),
		id:          uuid.NewString(),
		componentID: componentID,
	}

	i.add(s)
	go func() {
		<-ctx.Done()
		i.remove(s.id)
	}()

	return s
}

// Send the given record to all registered sessions.
// The method does not wait for consumers to get the records.
func (i *Inspector) Send(ctx context.Context, r record.Record) {
	// shortcut - we don't expect any sessions, so we check the atomic variable
	// before acquiring an actual lock
	if !i.hasSessions.Load() {
		return
	}

	// clone record only once, the listeners aren't expected to manipulate the records
	rClone := r.Clone()

	// locks are needed to make sure the `sessions` slice
	// is not modified as we're iterating over it
	i.lock.Lock()
	defer i.lock.Unlock()
	for _, s := range i.sessions {
		select {
		case s.C <- rClone:
		default:
			i.logger.
				Warn(ctx).
				Str(log.InspectorSessionID, s.id).
				Msg("session buffer full, record will be dropped")
		}
	}
}

func (i *Inspector) Close() {
	for k := range i.sessions {
		i.remove(k)
	}
}

// add a session with given ID to this Inspector.
func (i *Inspector) add(s *Session) {
	i.lock.Lock()
	defer i.lock.Unlock()

	i.sessions[s.id] = s
	i.hasSessions.Store(true)
	measure.InspectorsGauge.WithValues(s.componentID).Inc()

	i.logger.
		Info(context.Background()).
		Str(log.InspectorSessionID, s.id).
		Msg("session created")
}

// remove a session with given ID from this Inspector.
func (i *Inspector) remove(id string) {
	i.lock.Lock()
	defer i.lock.Unlock()

	s, ok := i.sessions[id]
	if !ok {
		return // session already removed
	}

	close(s.C)
	delete(i.sessions, id)
	if len(i.sessions) == 0 {
		i.hasSessions.Store(false)
	}
	measure.InspectorsGauge.WithValues(s.componentID).Dec()

	i.logger.
		Info(context.Background()).
		Str(log.InspectorSessionID, id).
		Msg("session removed")
}
