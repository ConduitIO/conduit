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
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type (
	// MessageStatus represents the state of the message (acked, nacked or open).
	MessageStatus int

	// ControlMessageType represents the type of a control message.
	ControlMessageType string
)

const (
	MessageStatusAcked MessageStatus = iota
	MessageStatusNacked
	MessageStatusOpen
)

var ErrUnexpectedMessageStatus = cerrors.New("unexpected message status")

// Message represents a single message flowing through a pipeline. Only a single
// node is allowed to hold a message and access its fields at a specific point
// in time, otherwise we could introduce race conditions.
type Message struct {
	// Ctx is the context in which the record was fetched. It should be used for
	// any function calls when processing the message. If the context is done
	// the message should be nacked as soon as possible and not processed
	// further.
	Ctx context.Context
	// Record represents a single record attached to the message.
	Record opencdc.Record

	// SourceID contains the source connector ID.
	SourceID string

	// controlMessageType is only populated for control messages. Control
	// messages are special messages injected into the message stream that can
	// change the behavior of a node and don't need to be acked/nacked.
	controlMessageType ControlMessageType

	// acked and nacked and are channels used to capture acks and nacks. When a
	// message is acked or nacked the corresponding channel is closed.
	acked  chan struct{}
	nacked chan struct{}

	filtered bool

	// handler is executed when Ack or Nack is called.
	handler StatusChangeHandler

	// hasNackHandler is true if at least one nack handler was registered.
	hasNackHandler bool

	// ackNackReturnValue is cached the first time Ack or Nack is executed.
	ackNackReturnValue error
	// initOnce is guarding the initialization logic of a message.
	initOnce sync.Once
	// ackNackOnce is guarding the acking/nacking logic of a message.
	ackNackOnce sync.Once
}

type NackMetadata struct {
	Reason error
	NodeID string
}

type (
	// StatusChangeHandler is executed when a message status changes. The
	// handlers are triggered by a call to either of these functions:
	// Message.Nack, Message.Ack. These functions will block until the handlers
	// finish handling the message and will return the error returned by the
	// handlers.
	// The function receives the message and the status change describing the
	// old and new message status as well as the reason for the status change in
	// case of a nack.
	StatusChangeHandler func(*Message, StatusChange) error

	// AckHandler is a variation of the StatusChangeHandler that is only called
	// when a message is acked. For more info see StatusChangeHandler.
	AckHandler func(*Message) error

	// NackHandler is a variation of the StatusChangeHandler that is only called
	// when a message is nacked. For more info see StatusChangeHandler.
	NackHandler func(*Message, NackMetadata) error
)

// StatusChange is passed to StatusChangeHandler when the status of a message
// changes.
type StatusChange struct {
	Old MessageStatus
	New MessageStatus
	// Reason contains the error that triggered the status change in case of a
	// nack.
	NackMetadata NackMetadata
}

// init initializes internal message fields.
func (m *Message) init() {
	m.initOnce.Do(func() {
		m.acked = make(chan struct{})
		m.nacked = make(chan struct{})
		// initialize empty status handler
		m.handler = func(msg *Message, change StatusChange) error { return nil }
	})
}

// ID returns a string representing a unique ID of this message. This is meant
// only for logging purposes.
func (m *Message) ID() string {
	return m.SourceID + "/" + string(m.Record.Position)
}

func (m *Message) ControlMessageType() ControlMessageType {
	return m.controlMessageType
}

// RegisterStatusHandler is used to register a function that will be called on
// any status change of the message. This function can only be called if the
// message status is open, otherwise it panics. Handlers are called in the
// reverse order of how they were registered.
func (m *Message) RegisterStatusHandler(h StatusChangeHandler) {
	m.init()

	if m.Status() != MessageStatusOpen {
		panic(cerrors.Errorf("BUG: tried to register handler on message %s, it has already been handled", m.ID()))
	}

	next := m.handler
	m.handler = func(msg *Message, change StatusChange) error {
		// all handlers are called and errors collected
		err1 := h(msg, change)
		err2 := next(msg, change)
		return cerrors.Join(err1, err2)
	}
}

// RegisterAckHandler is used to register a function that will be called when
// the message is acked. This function can only be called if the message status
// is open, otherwise it panics.
func (m *Message) RegisterAckHandler(h AckHandler) {
	m.RegisterStatusHandler(func(msg *Message, change StatusChange) error {
		if change.New != MessageStatusAcked {
			return nil // skip
		}
		return h(msg)
	})
}

// RegisterNackHandler is used to register a function that will be called when
// the message is nacked. This function can only be called if the message status
// is open, otherwise it panics.
func (m *Message) RegisterNackHandler(h NackHandler) {
	m.RegisterStatusHandler(func(msg *Message, change StatusChange) error {
		if change.New != MessageStatusNacked {
			return nil // skip
		}
		return h(msg, change.NackMetadata)
	})
	m.hasNackHandler = true
}

func (m *Message) notifyStatusHandlers(status MessageStatus, nackMetadata NackMetadata) error {
	return m.handler(m, StatusChange{
		Old:          m.Status(),
		New:          status,
		NackMetadata: nackMetadata,
	})
}

// Ack marks the message as acked, calls the corresponding status change
// handlers and closes the channel returned by Acked. Errors from ack handlers
// get collected and returned as a single error. If Ack returns an error, the
// caller node should stop processing new messages and return the error.
// Calling Ack after the message has already been nacked will panic, while
// subsequent calls to Ack on an acked message are a noop and return the same
// value.
func (m *Message) Ack() error {
	m.init()
	m.ackNackOnce.Do(func() {
		m.ackNackReturnValue = m.notifyStatusHandlers(MessageStatusAcked, NackMetadata{})
		close(m.acked)
	})
	if s := m.Status(); s != MessageStatusAcked {
		panic(cerrors.Errorf("BUG: message %s ack failed, status is %s: %w", m.ID(), s, ErrUnexpectedMessageStatus))
	}
	return m.ackNackReturnValue
}

// Nack marks the message as nacked, calls the registered status change handlers
// and closes the channel returned by Nacked. If no nack handlers were
// registered Nack will return an error. Errors from nack handlers get collected
// and returned as a single error. If Nack returns an error, the caller node
// should stop processing new messages and return the error.
// Calling Nack after the message has already been acked will panic, while
// subsequent calls to Nack on a nacked message are a noop and return the same
// value.
func (m *Message) Nack(reason error, nodeID string) error {
	m.init()
	m.ackNackOnce.Do(func() {
		m.ackNackReturnValue = m.notifyStatusHandlers(MessageStatusNacked, NackMetadata{
			Reason: reason,
			NodeID: nodeID,
		})
		if !m.hasNackHandler && m.ackNackReturnValue == nil {
			// we enforce at least one nack handler, otherwise nacks will go unnoticed
			m.ackNackReturnValue = cerrors.Errorf("no nack handler on message %s: %w", m.ID(), reason)
		}
		close(m.nacked)
	})
	if s := m.Status(); s != MessageStatusNacked {
		panic(cerrors.Errorf("BUG: message %s nack failed, status is %s: %w", m.ID(), s, ErrUnexpectedMessageStatus))
	}
	return m.ackNackReturnValue
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

// Clone returns a cloned message with the same content but separate ack and
// nack handling.
func (m *Message) Clone() *Message {
	return &Message{
		Ctx:    m.Ctx,
		Record: m.Record.Clone(),
	}
}

// Status returns the current message status.
func (m *Message) Status() MessageStatus {
	select {
	case <-m.acked:
		return MessageStatusAcked
	case <-m.nacked:
		return MessageStatusNacked
	default:
		return MessageStatusOpen
	}
}

// StatusError returns the error that was returned when the message was acked or
// nacked. If the message was successfully acked/nacked or it is still open the
// method returns nil.
func (m *Message) StatusError() error {
	switch m.Status() {
	case MessageStatusAcked:
		return m.Ack()
	case MessageStatusNacked:
		return m.Nack(nil, "")
	case MessageStatusOpen:
		return nil
	}

	return nil
}

// OpenMessagesTracker allows you to track messages until they reach the end of
// the pipeline.
type OpenMessagesTracker sync.WaitGroup

// Add will increase the counter in the wait group and register a status handler
// that will decrease the counter when the message is acked or nacked.
func (t *OpenMessagesTracker) Add(msg *Message) {
	(*sync.WaitGroup)(t).Add(1)
	msg.RegisterStatusHandler(
		func(msg *Message, change StatusChange) error {
			(*sync.WaitGroup)(t).Done()
			return nil
		},
	)
}

func (t *OpenMessagesTracker) Wait() {
	(*sync.WaitGroup)(t).Wait()
}
