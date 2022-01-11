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
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

func TestPubSubNodeBase_TriggerWithoutPubOrSub(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)

	n = &pubSubNodeBase{}
	n.Pub()
	trigger, cleanup, err = n.Trigger(ctx, logger)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)

	n = &pubSubNodeBase{}
	n.Sub(make(chan *Message))
	trigger, cleanup, err = n.Trigger(ctx, logger)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestPubSubNodeBase_PubTwice(t *testing.T) {
	n := &pubSubNodeBase{}
	n.Pub() // first one succeeds

	defer func() {
		assert.NotNil(t, recover())
	}()
	n.Pub() // second one should panic
}

func TestPubSubNodeBase_SubTwice(t *testing.T) {
	n := &pubSubNodeBase{}
	n.Sub(make(chan *Message)) // first one succeeds

	defer func() {
		assert.NotNil(t, recover())
	}()
	n.Sub(make(chan *Message)) // second one should panic
}

func TestPubSubNodeBase_TriggerTwice(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	n.Pub()
	n.Sub(make(chan *Message))
	trigger, cleanup, err := n.Trigger(ctx, logger)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	trigger, cleanup, err = n.Trigger(ctx, logger)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestPubSubNodeBase_TriggerSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	want := &Message{}
	go func() {
		// send the message to the in channel
		in <- want
	}()

	got, err := trigger()
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestPubSubNodeBase_TriggerClosedSubChannel(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the in channel is closed the next trigger should return no message
	close(in)

	got, err := trigger()
	assert.Ok(t, err)
	assert.Nil(t, got)
}

func TestPubSubNodeBase_TriggerCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the context is canceled trigger should return an error
	cancel()

	got, err := trigger()
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestPubSubNodeBase_Send(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	out := n.Pub()

	want := &Message{}
	go func() {
		err := n.Send(ctx, logger, want)
		assert.Ok(t, err)
	}()

	select {
	case <-ctx.Done():
		t.Fatal("did not expect context to get canceled before receiving message")
	case got := <-out:
		assert.Equal(t, want, got)
	}
}

func TestPubSubNodeBase_SendCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubSubNodeBase{}
	out := n.Pub()

	msg := &Message{}
	cancel() // context is cancelled before sending the message
	go func() {
		err := n.Send(ctx, logger, msg)
		assert.Error(t, err)
	}()

	time.Sleep(1 * time.Millisecond) // give runtime the ability to run the go routine

	select {
	case <-ctx.Done():
		// all good
	case <-out:
		t.Fatal("did not expect to receive a message from the pub channel")
	}
}

func TestPubNodeBase_TriggerWithoutPub(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil, nil)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestPubNodeBase_TriggerTwice(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()
	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	trigger, cleanup, err = n.Trigger(ctx, logger, nil, nil, nil)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestPubNodeBase_PubTwice(t *testing.T) {
	n := &pubNodeBase{}
	n.Pub() // first one succeeds

	defer func() {
		assert.NotNil(t, recover())
	}()
	n.Pub() // second one should panic
}

func TestPubNodeBase_TriggerSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	want := &Message{}
	trigger, cleanup, err := n.Trigger(
		ctx,
		logger,
		nil,
		nil,
		func(i interface{}) (*Message, error) {
			assert.Nil(t, i)
			return want, nil
		},
	)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	got, err := trigger()
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestPubNodeBase_TriggerWithTriggerChan(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	want := &Message{}
	trigger, cleanup, err := n.Trigger(
		ctx,
		logger,
		time.Tick(time.Millisecond*1),
		nil,
		func(i interface{}) (*Message, error) {
			_, ok := i.(time.Time)
			assert.True(t, ok, "expected a tick")
			return want, nil
		},
	)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	got, err := trigger()
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestPubNodeBase_TriggerWithClosedTriggerChan(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	triggerChan := make(chan struct{})
	trigger, cleanup, err := n.Trigger(ctx, logger, triggerChan, nil, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// a closed trigger chan should stop the trigger
	close(triggerChan)

	got, err := trigger()
	assert.Ok(t, err)
	assert.Nil(t, got)
}

func TestPubNodeBase_TriggerWithErrorChan(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	errChan := make(chan error, 1) // buffered channel to prevent locking
	trigger, cleanup, err := n.Trigger(ctx, logger, nil, errChan, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// an error in errChan should be returned by trigger
	wantErr := cerrors.New("test error")
	errChan <- wantErr

	got, err := trigger()
	assert.Nil(t, got)
	assert.Equal(t, wantErr, err)
}

func TestPubNodeBase_TriggerCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the context is cancelled trigger should return an error
	cancel()

	got, err := trigger()
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestPubNodeBase_Stop(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the node is stopped trigger should return no message
	wantErr := cerrors.New("my error")
	n.Stop(wantErr)

	got, err := trigger()
	assert.Equal(t, wantErr, err)
	assert.Nil(t, got)

	(&pubNodeBase{}).Stop(nil) // stop can be called on a non-running node
	n.Stop(nil)                // stop is idempotent
}

func TestPubNodeBase_Send(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	logger := log.Nop()

	n := &pubNodeBase{}
	out := n.Pub()

	want := &Message{}
	go func() {
		err := n.Send(ctx, logger, want)
		assert.Ok(t, err)
	}()

	select {
	case <-ctx.Done():
		t.Fatal("did not expect context to get cancelled before receiving message")
	case got := <-out:
		assert.Equal(t, want, got)
	}
}

func TestPubNodeBase_SendCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubNodeBase{}
	out := n.Pub()

	msg := &Message{}
	cancel() // context is cancelled before sending the message
	go func() {
		err := n.Send(ctx, logger, msg)
		assert.Error(t, err)
	}()

	time.Sleep(1 * time.Millisecond) // give runtime the ability to run the go routine

	select {
	case <-ctx.Done():
		// all good
	case <-out:
		t.Fatal("did not expect to receive a message from the pub channel")
	}
}

func TestSubNodeBase_TriggerWithoutSub(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestSubNodeBase_TriggerTwice(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	n.Sub(make(chan *Message))
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	trigger, cleanup, err = n.Trigger(ctx, logger, nil)
	assert.Nil(t, trigger)
	assert.Nil(t, cleanup)
	assert.Error(t, err)
}

func TestSubNodeBase_SubTwice(t *testing.T) {
	n := &subNodeBase{}
	n.Sub(make(chan *Message)) // first one succeeds

	defer func() {
		assert.NotNil(t, recover())
	}()
	n.Sub(make(chan *Message)) // second one should panic
}

func TestSubNodeBase_TriggerSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	want := &Message{}
	go func() {
		// send the message to the in channel
		in <- want
	}()

	got, err := trigger()
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestSubNodeBase_TriggerClosedSubChannel(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the in channel is closed the next trigger should return no message
	close(in)

	got, err := trigger()
	assert.Ok(t, err)
	assert.Nil(t, got)
}

func TestSubNodeBase_TriggerWithErrorChan(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	errChan := make(chan error, 1) // buffered channel to prevent locking
	trigger, cleanup, err := n.Trigger(ctx, logger, errChan)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// an error in errChan should be returned by trigger
	wantErr := cerrors.New("test error")
	errChan <- wantErr

	got, err := trigger()
	assert.Nil(t, got)
	assert.Equal(t, wantErr, err)
}

func TestSubNodeBase_TriggerCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	assert.Ok(t, err)
	assert.NotNil(t, trigger)
	assert.NotNil(t, cleanup)

	defer cleanup()

	// if the context is cancelled trigger should return an error
	cancel()

	got, err := trigger()
	assert.Error(t, err)
	assert.Nil(t, got)
}
