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

package stream

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/record"
)

// AckerNode is responsible for handling acknowledgments received from the
// destination and forwarding them to the correct message.
type AckerNode struct {
	Name        string
	Destination connector.Destination

	logger log.CtxLogger
	// cache stores the messages that are still waiting for an ack/nack.
	cache *positionMessageMap

	// start is closed once the first message is received in the destination node.
	start chan struct{}
	// stop is closed once the last message is received in the destination node.
	stop chan struct{}
	// initOnce initializes internal fields.
	initOnce sync.Once
	// startOnce closes start.
	startOnce sync.Once
	// stopOnce closes stop.
	stopOnce sync.Once
}

// init initializes AckerNode internal fields.
func (n *AckerNode) init() {
	n.initOnce.Do(func() {
		n.cache = &positionMessageMap{}
		n.start = make(chan struct{})
		n.stop = make(chan struct{})
	})
}

func (n *AckerNode) ID() string {
	return n.Name
}

// Run continuously fetches acks from the destination and forwards them to the
// correct message by calling Ack or Nack on that message.
func (n *AckerNode) Run(ctx context.Context) (err error) {
	n.logger.Trace(ctx).Msg("starting acker node")
	defer n.logger.Trace(ctx).Msg("acker node stopped")

	n.init()
	defer func() {
		dropAllErr := n.teardown()
		if err != nil {
			// we are already returning an error, just log this one
			n.logger.Err(ctx, dropAllErr).Msg("acker node stopped without processing all messages")
		} else {
			// return dropAllErr instead
			err = dropAllErr
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-n.stop:
		// destination actually stopped without ever receiving a message, we can
		// just return here
		return nil
	case <-n.start:
		// received first message for ack, destination is open now, we can
		// safely start listening to acks
		n.logger.Trace(ctx).Msg("start running acker node")
	}

	for {
		pos, err := n.Destination.Ack(ctx)
		if pos == nil {
			// empty position is returned only if an actual error happened
			if cerrors.Is(err, plugin.ErrStreamNotOpen) {
				// this means the plugin stopped, gracefully shut down
				n.logger.Debug(ctx).Msg("ack stream closed")
				return nil
			}
			return err
		}

		msg, ok := n.cache.LoadAndDelete(pos)
		if !ok {
			n.logger.Error(ctx).
				Str(log.RecordPositionField, pos.String()).
				Msg("received unexpected ack (could be an internal bug or a badly written connector), ignoring the ack and continuing, please report the issue to the Conduit team")
			continue
		}

		err = n.handleAck(msg, err)
		if err != nil {
			return err
		}
	}
}

// teardown will drop all messages still in the cache and return an error in
// case there were still unprocessed messages in the cache.
func (n *AckerNode) teardown() error {
	var dropped int
	n.cache.Range(func(pos record.Position, msg *Message) bool {
		msg.Drop()
		dropped++
		return true
	})
	if dropped > 0 {
		return cerrors.Errorf("dropped %d messages when stopping acker node", dropped)
	}
	return nil
}

// handleAck either acks or nacks the message, depending on the supplied error.
// If the nacking or acking fails, the message is dropped and the error is
// returned.
func (n *AckerNode) handleAck(msg *Message, err error) error {
	switch {
	case err != nil:
		n.logger.Trace(msg.Ctx).Err(err).Msg("nacking message")
		err = msg.Nack(err)
		if err != nil {
			msg.Drop()
			return cerrors.Errorf("error while nacking message: %w", err)
		}
	default:
		n.logger.Trace(msg.Ctx).Msg("acking message")
		err = msg.Ack()
		if err != nil {
			msg.Drop()
			return cerrors.Errorf("error while acking message: %w", err)
		}
	}
	return nil
}

// ExpectAck makes the handler aware of the message and signals to it that an
// ack for this message might be received at some point.
func (n *AckerNode) ExpectAck(msg *Message) error {
	// happens only once to signal Run that the destination is ready to be used.
	n.startOnce.Do(func() {
		n.init()
		close(n.start)
	})

	_, loaded := n.cache.LoadOrStore(msg.Record.Position, msg)
	if loaded {
		// we already have a message with the same position in the cache
		n.logger.Error(msg.Ctx).Msg("encountered two records with the same " +
			"position and can't differentiate them (could be that you are using " +
			"a pipeline with two same source connectors and they both produced " +
			"a record with the same position at the same time, could also be a " +
			"badly written source connector that doesn't assign unique positions " +
			"to records)")
		return cerrors.Errorf("encountered two records with the same position (%q)",
			msg.Record.Position.String())
	}
	return nil
}

// ForgetAndDrop signals the handler that an ack for this message won't be
// received, and it should remove it from its cache. In case an ack for this
// message wasn't yet received it drops the message, otherwise it does nothing.
func (n *AckerNode) ForgetAndDrop(msg *Message) {
	_, ok := n.cache.LoadAndDelete(msg.Record.Position)
	if !ok {
		// message wasn't found in the cache, looks like the message was already
		// acked / nacked
		return
	}
	msg.Drop()
}

// Wait can be used to wait for the count of outstanding acks to drop to 0 or
// the context gets canceled. Wait is expected to be the last function called on
// AckerNode, after Wait returns AckerNode will soon stop running.
func (n *AckerNode) Wait(ctx context.Context) {
	// happens only once to signal that the destination is stopping
	n.stopOnce.Do(func() {
		n.init()
		close(n.stop)
	})

	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		cacheSize := n.cache.Len()
		if cacheSize == 0 {
			return
		}
		n.logger.Debug(ctx).
			Int("remaining", cacheSize).
			Msg("waiting for acker node to process remaining acks")
		select {
		case <-ctx.Done():
			n.logger.Warn(ctx).
				Int("remaining", cacheSize).
				Msg("stopped waiting for acker node even though some acks may be remaining")
			return
		case <-t.C:
		}
	}
}

// SetLogger sets the logger.
func (n *AckerNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}

// positionMessageMap is like a Go map[record.Position]*Message but is safe for
// concurrent use by multiple goroutines. See documentation for sync.Map for
// more information (it's being used under the hood).
type positionMessageMap struct {
	m      sync.Map
	length uint32
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *positionMessageMap) LoadAndDelete(pos record.Position) (msg *Message, loaded bool) {
	val, loaded := m.m.LoadAndDelete(m.key(pos))
	if !loaded {
		return nil, false
	}
	atomic.AddUint32(&m.length, ^uint32(0)) // decrement
	return val.(*Message), loaded
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *positionMessageMap) LoadOrStore(pos record.Position, msg *Message) (actual *Message, loaded bool) {
	val, loaded := m.m.LoadOrStore(m.key(pos), msg)
	if !loaded {
		atomic.AddUint32(&m.length, 1) // increment
	}
	return val.(*Message), loaded
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's
// contents: no key will be visited more than once, but if the value for any key
// is stored or deleted concurrently, Range may reflect any mapping for that key
// from any point during the Range call.
//
// Range may be O(N) with the number of elements in the map even if f returns
// false after a constant number of calls.
func (m *positionMessageMap) Range(f func(pos record.Position, msg *Message) bool) {
	m.m.Range(func(key, value interface{}) bool {
		return f(record.Position(key.(string)), value.(*Message))
	})
}

// Len returns the number of elements in the map.
func (m *positionMessageMap) Len() int {
	return int(atomic.LoadUint32(&m.length))
}

// key takes a position and converts it into a hashable object that can be used
// as a key in a map.
func (m *positionMessageMap) key(pos record.Position) interface{} {
	return string(pos)
}
