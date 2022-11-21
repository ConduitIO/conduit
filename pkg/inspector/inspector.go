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

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
)

// Session wraps a channel of records and provides:
// 1. a way to send records to it asynchronously
// 2. a way to know if it's closed or not
type Session struct {
	C chan record.Record

	logger  log.CtxLogger
	onClose func()
}

func (s *Session) Close() {
	s.onClose()
	close(s.C)
}

// send sends a record to the session's channel.
// If the channel has already reached its capacity,
// the record will be ignored.
func (s *Session) send(ctx context.Context, r record.Record) {
	select {
	case s.C <- r:
	default:
		s.logger.
			Warn(ctx).
			Msg("session buffer full, record will be dropped")
	}
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
	lock       sync.Mutex
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

// Send sends the given record to all registered sessions.
// The method does not wait for consumers to get the records.
func (i *Inspector) Send(ctx context.Context, r record.Record) {
	// copy metadata, to prevent issues when concurrently accessing the metadata
	var meta record.Metadata
	if len(r.Metadata) != 0 {
		meta = make(record.Metadata, len(r.Metadata))
		for k, v := range r.Metadata {
			meta[k] = v
		}
	}

	// todo optimize this, as we have locks for every record.
	// locks are needed to make sure the `sessions` slice
	// is not modified as we're iterating over it
	i.lock.Lock()
	defer i.lock.Unlock()
	for _, s := range i.sessions {
		s.send(ctx, record.Record{
			Position:  r.Position,
			Operation: r.Operation,
			Metadata:  meta,
			Key:       r.Key,
			Payload:   r.Payload,
		})
	}
}

func (i *Inspector) NewSession(ctx context.Context) *Session {
	id := uuid.NewString()
	s := &Session{
		C:      make(chan record.Record, i.bufferSize),
		logger: i.logger.WithComponent("inspector.Session"),
		onClose: func() {
			i.remove(id)
		},
	}
	go func() {
		<-ctx.Done()
		s.logger.
			Info(context.Background()).
			Msgf("context canceled: %v", ctx.Err())
		s.Close()
	}()

	i.lock.Lock()
	defer i.lock.Unlock()

	i.sessions[id] = s
	i.logger.
		Info(context.Background()).
		Str("session_id", id).
		Msg("session created")
	return s
}

// remove removes a session with given ID from this Inspector.
func (i *Inspector) remove(id string) {
	i.lock.Lock()
	defer i.lock.Unlock()

	delete(i.sessions, id)
	i.logger.
		Info(context.Background()).
		Str("session_id", id).
		Msg("session removed")
}
