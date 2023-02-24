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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
)

const DefaultBufferSize = 1000

// Session wraps a channel of records and provides:
// 1. a way to send records to it asynchronously
// 2. a way to know if it's closed or not
type Session struct {
	C chan record.Record

	closed bool
	logger log.CtxLogger
}

var errSessionClosed = cerrors.New("session closed")

func (s *Session) close(ctx context.Context) bool {
	if !s.closed {
		s.closed = true
		close(s.C)
		s.logger.Info(ctx).Msg("session closed")
	}
	return false
}

// send a record to the session's channel.
// If the channel has already reached its capacity,
// the record will be ignored.
func (s *Session) send(ctx context.Context, r record.Record) error {
	if s.closed {
		return errSessionClosed
	}

	select {
	case s.C <- r:
		return nil
	default:
		s.logger.Warn(ctx).Msg("session buffer full, record was dropped")
		return nil
	}
}

// Inspector is attached to an inspectable pipeline component
// and makes returns records coming in or out of the component.
// An Inspector is a "proxy" between the pipeline component being
// inspected and the API, which broadcasts records to all clients.
type Inspector struct {
	sessions chan *Session
	// guards access to all Session objects in sessions channel
	lock       sync.Mutex
	logger     log.CtxLogger
	bufferSize int
}

func New(logger log.CtxLogger, bufferSize int) *Inspector {
	return &Inspector{
		sessions:   make(chan *Session, 100), // TODO make session count configurable
		logger:     logger.WithComponent("inspector.Inspector"),
		bufferSize: bufferSize,
	}
}

// Send the given record to all registered sessions.
// The method does not wait for consumers to get the records.
func (i *Inspector) Send(ctx context.Context, r record.Record) {
	sessionCount := len(i.sessions)
	if sessionCount == 0 {
		return // no open sessions
	}

	// copy metadata, to prevent issues when concurrently accessing the metadata
	var meta record.Metadata
	if len(r.Metadata) != 0 {
		meta = make(record.Metadata, len(r.Metadata))
		for k, v := range r.Metadata {
			meta[k] = v
		}
	}

	// lock all sessions
	i.lock.Lock()
	defer i.lock.Unlock()
	for s := range i.sessions {
		sessionCount--
		err := s.send(ctx, r)

		if err == nil {
			// we only put open sessions back into the queue, if there was an
			// error the session is closed
			i.sessions <- s
		}
		if sessionCount == 0 {
			break
		}
	}
}

func (i *Inspector) NewSession(ctx context.Context) *Session {
	id := uuid.NewString()
	s := &Session{
		C: make(chan record.Record, i.bufferSize),
		logger: func() log.CtxLogger {
			logger := i.logger.WithComponent("inspector.Session")
			logger.Logger = logger.With().Str(log.InspectorSessionID, id).Logger()
			return logger
		}(),
	}
	go func() {
		<-ctx.Done()
		// lock sessions
		i.lock.Lock()
		defer i.lock.Unlock()
		s.close(ctx)
	}()

	i.lock.Lock()
	defer i.lock.Unlock()
	select {
	case i.sessions <- s:
		i.logger.Info(ctx).Str(log.InspectorSessionID, id).Msg("session created")
	default:
		panic("no space for more sessions") // TODO return error
		// TODO we could also clean out any closed sessions in the queue before deciding there's no space
	}

	return s
}

func (i *Inspector) Close() {
	// lock sessions
	i.lock.Lock()
	defer i.lock.Unlock()
	for {
		select {
		case s := <-i.sessions:
			s.close(context.Background())
		default:
			return // no more sessions
		}
	}
}
