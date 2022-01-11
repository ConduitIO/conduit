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

//go:generate stringer -type=MessageStatus -trimprefix MessageStatus

package stream

import (
	"context"
	"fmt"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

// MessageStatus represents the state of the message (acked, nacked, dropped or open).
type MessageStatus int

const (
	MessageStatusAcked MessageStatus = iota
	MessageStatusNacked
	MessageStatusOpen
	MessageStatusDropped
)

var (
	ErrMessageDropped          = cerrors.New("message is dropped")
	ErrUnexpectedMessageStatus = cerrors.New("unexpected message status")
)

// Message represents a single message flowing through a pipeline.
type Message struct {
	// Ctx is the context in which the record was fetched. It should be used for
	// any function calls when processing the message. If the context is done
	// the message should be dropped as soon as possible and not processed
	// further.
	Ctx context.Context
	// Record represents a single record attached to the message.
	Record record.Record

	// acked, nacked and dropped are channels used to capture acks, nacks and
	// drops. When a message is acked, nacked or dropped the corresponding
	// channel is closed.
	acked   chan struct{}
	nacked  chan struct{}
	dropped chan struct{}

	// handler is executed when Ack, Nack or Drop is called.
	handler StatusChangeHandler
	// hasNackHandler is true if at least one nack handler was registered.
	hasNackHandler bool

	// ackNackReturnValue is cached the first time Ack, Nack or Drop is executed.
	ackNackReturnValue error

	// initOnce is guarding the initialization logic of a message.
	initOnce sync.Once
	// ackNackDropOnce is guarding the acking/nacking/dropping logic of a message.
	ackNackDropOnce sync.Once
	// handlerGuard guards fields ackHandlers and nackHandlers.
	handlerGuard sync.Mutex
}

type (
	// StatusChangeHandler is executed when a message status changes. The handlers
	// are triggered by a call to either of these functions: Message.Nack,
	// Message.Ack, Message.Drop. These functions will block until the handlers
	// finish handling the message and will return the error returned by the
	// handlers.
	// The function receives the message and the status change describing the old
	// and new message status as well as the reason for the status change in case of
	// a nack or drop.
	StatusChangeHandler func(*Message, StatusChange) error

	// StatusChangeMiddleware can be registered on a message and will be executed in
	// case of a status change (see StatusChangeHandler). Middlewares are called in
	// the reverse order of how they were registered.
	// The middleware has two options when processing a message status change:
	//   - If it successfully processed the status change it should call the next
	//     handler and return its error. The handler may inspect the error and act
	//     accordingly, but it must return that error (or another error that
	//     contains it). It must not return an error if the next handler was called
	//     and it returned nil.
	//   - If it failed to process the status change successfully it must not call
	//     the next handler but instead return an error right away.
	// Applying these rules means each middleware can be sure that all middlewares
	// before it processed the status change successfully.
	StatusChangeMiddleware func(*Message, StatusChange, StatusChangeHandler) error

	// AckHandler is a variation of the StatusChangeHandler that is only called
	// when a message is acked. For more info see StatusChangeHandler.
	AckHandler func(*Message) error
	// AckMiddleware is a variation of the StatusChangeMiddleware that is only
	// called when a message is acked. For more info see StatusChangeMiddleware.
	AckMiddleware func(*Message, AckHandler) error

	// NackHandler is a variation of the StatusChangeHandler that is only called
	// when a message is nacked. For more info see StatusChangeHandler.
	NackHandler func(*Message, error) error
	// NackMiddleware is a variation of the StatusChangeMiddleware that is only
	// called when a message is nacked. For more info see StatusChangeMiddleware.
	NackMiddleware func(*Message, error, NackHandler) error

	// DropHandler is a variation of the StatusChangeHandler that is only called
	// when a message is dropped. For more info see StatusChangeHandler.
	DropHandler func(*Message, error)
	// DropMiddleware is a variation of the StatusChangeMiddleware that is only
	// called when a message is dropped. For more info see StatusChangeMiddleware.
	DropMiddleware func(*Message, error, DropHandler)
)

// StatusChange is passed to StatusChangeMiddleware and StatusChangeHandler when
// the status of a message changes.
type StatusChange struct {
	Old MessageStatus
	New MessageStatus
	// Reason contains the error that triggered the status change in case of a
	// nack or drop.
	Reason error
}

// init initializes internal message fields.
func (m *Message) init() {
	m.initOnce.Do(func() {
		m.acked = make(chan struct{})
		m.nacked = make(chan struct{})
		m.dropped = make(chan struct{})
		// initialize empty status handler
		m.handler = func(msg *Message, change StatusChange) error { return nil }
	})
}

// ID returns a string representing a unique ID of this message. This is meant
// only for logging purposes.
func (m *Message) ID() string {
	return fmt.Sprintf("%s/%s", m.Record.SourceID, m.Record.Position)
}

// RegisterStatusHandler is used to register a function that will be called on
// any status change of the message. This function can only be called if the
// message status is open, otherwise it panics. Middlewares are called in the
// reverse order of how they were registered.
func (m *Message) RegisterStatusHandler(mw StatusChangeMiddleware) {
	m.init()
	m.handlerGuard.Lock()
	defer m.handlerGuard.Unlock()

	if m.Status() != MessageStatusOpen {
		panic(cerrors.Errorf("BUG: tried to register handler on message %s, it has already been handled", m.ID()))
	}

	next := m.handler
	m.handler = func(msg *Message, change StatusChange) error {
		return mw(msg, change, next)
	}
}

// RegisterAckHandler is used to register a function that will be called when
// the message is acked. This function can only be called if the message status
// is open, otherwise it panics.
func (m *Message) RegisterAckHandler(mw AckMiddleware) {
	m.RegisterStatusHandler(func(msg *Message, change StatusChange, next StatusChangeHandler) error {
		if change.New != MessageStatusAcked {
			return next(msg, change)
		}
		return mw(msg, func(msg *Message) error {
			return next(msg, change)
		})
	})
}

// RegisterNackHandler is used to register a function that will be called when
// the message is nacked. This function can only be called if the message status
// is open, otherwise it panics.
func (m *Message) RegisterNackHandler(mw NackMiddleware) {
	m.RegisterStatusHandler(func(msg *Message, change StatusChange, next StatusChangeHandler) error {
		if change.New != MessageStatusNacked {
			return next(msg, change)
		}
		return mw(msg, change.Reason, func(msg *Message, reason error) error {
			return next(msg, change)
		})
	})
	m.hasNackHandler = true
}

// RegisterDropHandler is used to register a function that will be called when
// the message is dropped. This function can only be called if the message
// status is open, otherwise it panics.
func (m *Message) RegisterDropHandler(mw DropMiddleware) {
	m.RegisterStatusHandler(func(msg *Message, change StatusChange, next StatusChangeHandler) error {
		if change.New != MessageStatusDropped {
			return next(msg, change)
		}
		mw(msg, change.Reason, func(msg *Message, reason error) {
			err := next(msg, change)
			if err != nil {
				panic(cerrors.Errorf("BUG: drop handlers should never return an error (message %s): %w", msg.ID(), err))
			}
		})
		return nil
	})
}

func (m *Message) notifyStatusHandlers(status MessageStatus, reason error) error {
	m.handlerGuard.Lock()
	defer m.handlerGuard.Unlock()

	return m.handler(m, StatusChange{
		Old:    m.Status(),
		New:    status,
		Reason: reason,
	})
}

// Ack marks the message as acked, calls the corresponding status change
// handlers and closes the channel returned by Acked. If an ack handler returns
// an error, the message is dropped instead, which means that registered status
// change handlers are again notified about the drop and the channel returned by
// Dropped is closed instead.
// Calling Ack after the message has already been nacked will panic, while
// subsequent calls to Ack on an acked or dropped message are a noop and return
// the same value.
func (m *Message) Ack() error {
	m.init()
	m.ackNackDropOnce.Do(func() {
		m.ackNackReturnValue = m.notifyStatusHandlers(MessageStatusAcked, nil)
		if m.ackNackReturnValue != nil {
			// unsuccessful ack, message is dropped
			_ = m.notifyStatusHandlers(MessageStatusDropped, m.ackNackReturnValue)
			close(m.dropped)
			return
		}
		close(m.acked)
	})
	if s := m.Status(); s != MessageStatusAcked && s != MessageStatusDropped {
		panic(cerrors.Errorf("BUG: message %s ack failed, status is %s: %w", m.ID(), s, ErrUnexpectedMessageStatus))
	}
	return m.ackNackReturnValue
}

// Nack marks the message as nacked, calls the registered status change handlers
// and closes the channel returned by Nacked. If no nack handlers were
// registered or a nack handler returns an error, the message is dropped
// instead, which means that registered status change handlers are again
// notified about the drop and the channel returned by Dropped is closed
// instead.
// Calling Nack after the message has already been acked will panic, while
// subsequent calls to Nack on a nacked or dropped message are a noop and return
// the same value.
func (m *Message) Nack(reason error) error {
	m.init()
	m.ackNackDropOnce.Do(func() {
		if !m.hasNackHandler {
			// we enforce at least one nack handler, otherwise nacks will go unnoticed
			m.ackNackReturnValue = cerrors.Errorf("no nack handler on message %s: %w", m.ID(), reason)
		} else {
			m.ackNackReturnValue = m.notifyStatusHandlers(MessageStatusNacked, reason)
		}
		if m.ackNackReturnValue != nil {
			// unsuccessful nack, message is dropped
			_ = m.notifyStatusHandlers(MessageStatusDropped, m.ackNackReturnValue)
			close(m.dropped)
			return
		}
		close(m.nacked)
	})
	if s := m.Status(); s != MessageStatusNacked && s != MessageStatusDropped {
		panic(cerrors.Errorf("BUG: message %s nack failed, status is %s: %w", m.ID(), s, ErrUnexpectedMessageStatus))
	}
	return m.ackNackReturnValue
}

// Drop marks the message as dropped, calls the registered status change
// handlers and closes the channel returned by Dropped.
// Calling Drop after the message has already been acked or nacked will panic,
// while subsequent calls to Drop on a dropped message are a noop.
func (m *Message) Drop() {
	m.init()
	m.ackNackDropOnce.Do(func() {
		m.ackNackReturnValue = ErrMessageDropped
		err := m.notifyStatusHandlers(MessageStatusDropped, m.ackNackReturnValue)
		if err != nil {
			panic(cerrors.Errorf("BUG: drop handlers should never return an error (message %s): %w", m.ID(), err))
		}
		close(m.dropped)
	})
	if s := m.Status(); s != MessageStatusDropped {
		panic(cerrors.Errorf("BUG: message %s drop failed, status is %s: %w", m.ID(), s, ErrUnexpectedMessageStatus))
	}
}

// Acked returns a channel that's closed when the message has been acked.
// Successive calls to Acked return the same value. This function can be used to
// wait for a message to be acked without notifying the acker.
func (m *Message) Acked() <-chan struct{} {
	m.init()
	return m.acked
}

// Nacked returns a channel that's closed when the message has been nacked.
// Successive calls to Nacked return the same value. This function can be used
// to wait for a message to be nacked without notifying the nacker.
func (m *Message) Nacked() <-chan struct{} {
	m.init()
	return m.nacked
}

// Dropped returns a channel that's closed when the message has been dropped.
// Successive calls to Dropped return the same value. This function can be used
// to wait for a message to be dropped without notifying the dropper.
func (m *Message) Dropped() <-chan struct{} {
	m.init()
	return m.dropped
}

// Clone returns a cloned message with the same content but separate ack and
// nack handling.
func (m *Message) Clone() *Message {
	return &Message{
		Ctx:    m.Ctx,
		Record: m.Record,
	}
}

// Status returns the current message status.
func (m *Message) Status() MessageStatus {
	select {
	case <-m.acked:
		return MessageStatusAcked
	case <-m.nacked:
		return MessageStatusNacked
	case <-m.dropped:
		return MessageStatusDropped
	default:
		return MessageStatusOpen
	}
}
