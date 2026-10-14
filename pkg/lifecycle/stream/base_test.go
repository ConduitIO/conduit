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
	"runtime"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestPubSubNodeBase_TriggerWithoutPubOrSub(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)

	n = &pubSubNodeBase{}
	n.Pub()
	trigger, cleanup, err = n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)

	n = &pubSubNodeBase{}
	n.Sub(make(chan *Message))
	trigger, cleanup, err = n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestPubSubNodeBase_PubTwice(t *testing.T) {
	is := is.New(t)
	n := &pubSubNodeBase{}
	n.Pub() // first one succeeds

	defer func() {
		is.True(recover() != nil)
	}()
	n.Pub() // second one should panic
}

func TestPubSubNodeBase_SubTwice(t *testing.T) {
	is := is.New(t)
	n := &pubSubNodeBase{}
	n.Sub(make(chan *Message)) // first one succeeds

	defer func() {
		is.True(recover() != nil)
	}()
	n.Sub(make(chan *Message)) // second one should panic
}

func TestPubSubNodeBase_TriggerTwice(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	n.Pub()
	n.Sub(make(chan *Message))
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	trigger, cleanup, err = n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestPubSubNodeBase_TriggerSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	want := &Message{}
	go func() {
		// send the message to the in channel
		in <- want
	}()

	got, err := trigger()
	is.NoErr(err)
	is.Equal(want, got)
}

func TestPubSubNodeBase_TriggerClosedSubChannel(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// if the in channel is closed the next trigger should return no message
	close(in)

	got, err := trigger()
	is.NoErr(err)
	is.True(got == nil)
}

func TestPubSubNodeBase_TriggerCancelledContext(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubSubNodeBase{}
	in := make(chan *Message)
	n.Sub(in)
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// if the context is canceled trigger should return an error
	cancel()

	got, err := trigger()
	is.True(err != nil)
	is.True(got == nil)
}

func TestPubSubNodeBase_Send(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	logger := log.Nop()

	n := &pubSubNodeBase{}
	out := n.Pub()

	want := &Message{}
	go func() {
		err := n.Send(ctx, logger, want)
		is.NoErr(err)
	}()

	select {
	case <-ctx.Done():
		t.Fatal("did not expect context to get canceled before receiving message")
	case got := <-out:
		is.Equal(want, got)
	}
}

func TestPubSubNodeBase_SendCancelledContext(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubSubNodeBase{}
	out := n.Pub()

	msg := &Message{}
	cancel() // context is cancelled before sending the message
	go func() {
		err := n.Send(ctx, logger, msg)
		is.True(err != nil)
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
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestPubNodeBase_TriggerTwice(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()
	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	trigger, cleanup, err = n.Trigger(ctx, logger, nil, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestPubNodeBase_PubTwice(t *testing.T) {
	is := is.New(t)
	n := &pubNodeBase{}
	n.Pub() // first one succeeds

	defer func() {
		is.True(recover() != nil)
	}()
	n.Pub() // second one should panic
}

func TestPubNodeBase_TriggerSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	want := []*Message{{}}
	trigger, cleanup, err := n.Trigger(
		ctx,
		logger,
		nil,
		func(context.Context) ([]*Message, error) {
			return want, nil
		},
	)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	got, err := trigger()
	is.NoErr(err)
	is.Equal(want[0], got)
}

func TestPubNodeBase_TriggerWithErrorChan(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	errChan := make(chan error, 1) // buffered channel to prevent locking
	trigger, cleanup, err := n.Trigger(ctx, logger, errChan, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// an error in errChan should be returned by trigger
	wantErr := cerrors.New("test error")
	errChan <- wantErr

	got, err := trigger()
	is.True(got == nil)
	is.Equal(wantErr, err)
}

func TestPubNodeBase_TriggerCancelledContext(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	trigger, cleanup, err := n.Trigger(ctx, logger, nil, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// if the context is cancelled trigger should return an error
	cancel()

	got, err := trigger()
	is.True(err != nil)
	is.True(got == nil)
}

func TestPubNodeBase_Send(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	logger := log.Nop()

	n := &pubNodeBase{}
	out := n.Pub()

	want := &Message{}
	go func() {
		err := n.Send(ctx, logger, want)
		is.NoErr(err)
	}()

	select {
	case <-ctx.Done():
		t.Fatal("did not expect context to get cancelled before receiving message")
	case got := <-out:
		is.Equal(want, got)
	}
}

func TestPubNodeBase_SendCancelledContext(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &pubNodeBase{}
	out := n.Pub()

	msg := &Message{}
	cancel() // context is cancelled before sending the message
	go func() {
		err := n.Send(ctx, logger, msg)
		is.True(err != nil)
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
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestSubNodeBase_TriggerTwice(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	n.Sub(make(chan *Message))
	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	trigger, cleanup, err = n.Trigger(ctx, logger, nil)
	is.True(trigger == nil)
	is.True(cleanup == nil)
	is.True(err != nil)
}

func TestSubNodeBase_SubTwice(t *testing.T) {
	is := is.New(t)
	n := &subNodeBase{}
	n.Sub(make(chan *Message)) // first one succeeds

	defer func() {
		is.True(recover() != nil)
	}()
	n.Sub(make(chan *Message)) // second one should panic
}

func TestSubNodeBase_TriggerSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	want := &Message{}
	go func() {
		// send the message to the in channel
		in <- want
	}()

	got, err := trigger()
	is.NoErr(err)
	is.Equal(want, got)
}

func TestSubNodeBase_TriggerClosedSubChannel(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// if the in channel is closed the next trigger should return no message
	close(in)

	got, err := trigger()
	is.NoErr(err)
	is.True(got == nil)
}

func TestSubNodeBase_TriggerWithErrorChan(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	errChan := make(chan error, 1) // buffered channel to prevent locking
	trigger, cleanup, err := n.Trigger(ctx, logger, errChan)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// an error in errChan should be returned by trigger
	wantErr := cerrors.New("test error")
	errChan <- wantErr

	got, err := trigger()
	is.True(got == nil)
	is.Equal(wantErr, err)
}

func TestSubNodeBase_TriggerCancelledContext(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.Nop()

	n := &subNodeBase{}
	in := make(chan *Message)
	n.Sub(in)

	trigger, cleanup, err := n.Trigger(ctx, logger, nil)
	is.NoErr(err)
	is.True(trigger != nil)
	is.True(cleanup != nil)

	defer cleanup()

	// if the context is cancelled trigger should return an error
	cancel()

	got, err := trigger()
	is.True(err != nil)
	is.True(got == nil)
}

// TestPubNodeBase_InjectControlMessage_AfterTriggerFailed guards #2969: once
// the trigger has returned its final error nobody reads the internal message
// channel, so injecting must give up instead of blocking, and it must not
// keep cleanup from taking the lock.
func TestPubNodeBase_InjectControlMessage_AfterTriggerFailed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background() // no deadline
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()

	errChan := make(chan error, 1)
	trigger, cleanup, err := n.Trigger(ctx, logger, errChan, nil)
	is.NoErr(err)

	wantErr := cerrors.New("source failed")
	errChan <- wantErr
	_, err = trigger()
	is.Equal(wantErr, err)

	injectErr := make(chan error, 1)
	go func() {
		injectErr <- n.InjectControlMessage(ctx, ControlMessageStopSourceNode, opencdc.Record{})
	}()
	select {
	case err := <-injectErr:
		is.True(err != nil)
	case <-time.After(3 * time.Second):
		t.Fatal("InjectControlMessage blocked after the trigger failed")
	}

	cleaned := make(chan struct{})
	go func() { cleanup(); close(cleaned) }()
	select {
	case <-cleaned:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup blocked")
	}

	// after cleanup the node is not running anymore
	is.True(n.InjectControlMessage(ctx, ControlMessageStopSourceNode, opencdc.Record{}) != nil)
}

// TestPubNodeBase_InjectControlMessage_BlockedWhenCleanupStarts covers the
// exact interleaving of #2969 at the base level: an inject is blocked on the
// send when cleanup runs. Cleanup must complete and the inject must return.
func TestPubNodeBase_InjectControlMessage_BlockedWhenCleanupStarts(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	n := &pubNodeBase{}
	n.Pub()
	_, cleanup, err := n.Trigger(ctx, logger, nil, nil)
	is.NoErr(err)

	injectErr := make(chan error, 1)
	go func() {
		injectErr <- n.InjectControlMessage(ctx, ControlMessageStopSourceNode, opencdc.Record{})
	}()
	time.Sleep(50 * time.Millisecond) // let the inject block on the send

	cleaned := make(chan struct{})
	go func() { cleanup(); close(cleaned) }()
	select {
	case <-cleaned:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup blocked behind InjectControlMessage")
	}
	select {
	case err := <-injectErr:
		is.True(err != nil)
	case <-time.After(3 * time.Second):
		t.Fatal("InjectControlMessage still blocked after cleanup")
	}
}

// TestPubNodeBase_NoGoroutineLeakAfterError checks the goroutines spawned by
// Trigger (message fetcher, external error forwarder) exit once the trigger
// failed and cleanup ran, even if they were blocked handing something over.
func TestPubNodeBase_NoGoroutineLeakAfterError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background() // never canceled: only done can release them
	logger := log.Nop()

	before := runtime.NumGoroutine()

	n := &pubNodeBase{}
	n.Pub()

	errChan := make(chan error, 2)
	trigger, cleanup, err := n.Trigger(ctx, logger, errChan, func(context.Context) ([]*Message, error) {
		// more than one message so the fetcher blocks on the second send
		return []*Message{{}, {}}, nil
	})
	is.NoErr(err)

	// the fetcher delivers a message, then blocks sending the next one
	_, err = trigger()
	is.NoErr(err)

	// two errors: the trigger consumes one, the forwarder blocks on the other
	wantErr := cerrors.New("source failed")
	errChan <- wantErr
	errChan <- wantErr
	for {
		_, err = trigger()
		if err != nil {
			break
		}
	}
	cleanup()

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	is.True(runtime.NumGoroutine() <= before) // goroutines leaked
}
