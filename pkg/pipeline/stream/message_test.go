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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

func TestMessage_Ack_WithoutHandler(t *testing.T) {
	var msg Message

	assertMessageIsOpen(t, &msg)

	err := msg.Ack()
	if err != nil {
		t.Fatalf("ack did not expect error, got %v", err)
	}
	assertMessageIsAcked(t, &msg)

	// doing the same thing again shouldn't do anything
	err = msg.Ack()
	if err != nil {
		t.Fatalf("ack did not expect error, got %v", err)
	}
	assertMessageIsAcked(t, &msg)
}

func TestMessage_Ack_WithHandler(t *testing.T) {
	var (
		msg Message

		ackedMessageHandlerCallCount int
	)

	msg.RegisterAckHandler(func(*Message) error {
		ackedMessageHandlerCallCount++
		return nil
	})

	err := msg.Ack()
	if err != nil {
		t.Fatalf("ack did not expect error, got %v", err)
	}
	assertMessageIsAcked(t, &msg)
	if ackedMessageHandlerCallCount != 1 {
		t.Fatalf("expected acked message handler to be called once, got %d calls", ackedMessageHandlerCallCount)
	}

	// doing the same thing again shouldn't do anything
	err = msg.Ack()
	if err != nil {
		t.Fatalf("ack did not expect error, got %v", err)
	}
	assertMessageIsAcked(t, &msg)
	if ackedMessageHandlerCallCount != 1 {
		t.Fatalf("expected acked message handler to be called once, got %d calls", ackedMessageHandlerCallCount)
	}

	defer func() {
		if recover() == nil {
			t.Fatalf("expected msg.Nack to panic")
		}
	}()
	_ = msg.Nack(nil) // nacking the message should panic
}

func TestMessage_Ack_WithFailingHandler(t *testing.T) {
	var (
		msg     Message
		wantErr = cerrors.New("oops")

		ackedMessageHandlerCallCount  int
		statusMessageHandlerCallCount int
	)

	{
		// first handler should still be called
		msg.RegisterAckHandler(func(*Message) error {
			ackedMessageHandlerCallCount++
			return nil
		})
		// second handler fails
		msg.RegisterAckHandler(func(*Message) error {
			return wantErr
		})
		// third handler should work as expected
		msg.RegisterAckHandler(func(msg *Message) error {
			ackedMessageHandlerCallCount++
			return nil
		})
		// fourth handler should be called once
		msg.RegisterStatusHandler(func(msg *Message, change StatusChange) error {
			statusMessageHandlerCallCount++
			return nil
		})
		// nack handler should not be called
		msg.RegisterNackHandler(func(*Message, error) error {
			t.Fatalf("did not expect nack handler to be called")
			return nil
		})
	}

	// doing the same thing twice should have the same result
	for i := 0; i < 2; i++ {
		err := msg.Ack()
		if err != wantErr {
			t.Fatalf("ack expected error %v, got: %v", wantErr, err)
		}
		assertMessageIsAcked(t, &msg)
		if ackedMessageHandlerCallCount != 2 {
			t.Fatalf("expected acked message handler to be called twice, got %d calls", ackedMessageHandlerCallCount)
		}
		if statusMessageHandlerCallCount != 1 {
			t.Fatalf("expected status message handler to be called once, got %d calls", statusMessageHandlerCallCount)
		}
	}
}

func TestMessage_Nack_WithoutHandler(t *testing.T) {
	var msg Message

	assertMessageIsOpen(t, &msg)

	// nack should fail because there is no handler for the nack
	err1 := msg.Nack(cerrors.New("reason"))
	if err1 == nil {
		t.Fatal("nack expected error, got nil")
	}
	assertMessageIsNacked(t, &msg)

	// nacking again should return the same error
	err2 := msg.Nack(cerrors.New("reason"))
	if err1 != err2 {
		t.Fatalf("nack expected error %v, got %v", err1, err2)
	}
	assertMessageIsNacked(t, &msg)
}

func TestMessage_Nack_WithHandler(t *testing.T) {
	var (
		msg     Message
		wantErr = cerrors.New("test error")

		nackedMessageHandlerCallCount int
	)

	msg.RegisterNackHandler(func(msg *Message, err error) error {
		nackedMessageHandlerCallCount++
		if err != wantErr {
			t.Fatalf("nacked message handler, expected err %v, got %v", wantErr, err)
		}
		return nil
	})

	err := msg.Nack(wantErr)
	if err != nil {
		t.Fatalf("nack did not expect error, got %v", err)
	}
	assertMessageIsNacked(t, &msg)
	if nackedMessageHandlerCallCount != 1 {
		t.Fatalf("expected nacked message handler to be called once, got %d calls", nackedMessageHandlerCallCount)
	}

	// nacking again shouldn't do anything
	err = msg.Nack(nil)
	if err != nil {
		t.Fatalf("nack did not expect error, got %v", err)
	}
	assertMessageIsNacked(t, &msg)
	if nackedMessageHandlerCallCount != 1 {
		t.Fatalf("expected nacked message handler to be called once, got %d calls", nackedMessageHandlerCallCount)
	}
}

func TestMessage_Nack_WithFailingHandler(t *testing.T) {
	var (
		msg     Message
		wantErr = cerrors.New("oops")

		nackedMessageHandlerCallCount int
		statusMessageHandlerCallCount int
	)

	{
		// first handler should still be called
		msg.RegisterNackHandler(func(*Message, error) error {
			nackedMessageHandlerCallCount++
			return nil
		})
		// second handler fails
		msg.RegisterNackHandler(func(*Message, error) error {
			return wantErr
		})
		// third handler should work as expected
		msg.RegisterNackHandler(func(msg *Message, reason error) error {
			nackedMessageHandlerCallCount++
			return nil
		})
		// fourth handler should be called once
		msg.RegisterStatusHandler(func(msg *Message, change StatusChange) error {
			statusMessageHandlerCallCount++
			return nil
		})
		// ack handler should not be called
		msg.RegisterAckHandler(func(*Message) error {
			t.Fatalf("did not expect ack handler to be called")
			return nil
		})
	}

	// doing the same thing twice should have the same result
	for i := 0; i < 2; i++ {
		err := msg.Nack(nil)
		if err != wantErr {
			t.Fatalf("nack expected error %v, got: %v", wantErr, err)
		}
		assertMessageIsNacked(t, &msg)
		if nackedMessageHandlerCallCount != 2 {
			t.Fatalf("expected nacked message handler to be called twice, got %d calls", nackedMessageHandlerCallCount)
		}
		if statusMessageHandlerCallCount != 1 {
			t.Fatalf("expected status message handler to be called once, got %d calls", statusMessageHandlerCallCount)
		}
	}
}

func TestMessage_StatusChangeTwice(t *testing.T) {
	assertAckPanics := func(msg *Message) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected msg.Ack to panic")
			}
		}()
		_ = msg.Ack()
	}
	assertNackPanics := func(msg *Message) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected msg.Nack to panic")
			}
		}()
		_ = msg.Nack(nil)
	}

	// nack after the message is acked should panic
	t.Run("acked message", func(t *testing.T) {
		var msg Message
		err := msg.Ack()
		if err != nil {
			t.Fatalf("ack did not expect error, got %v", err)
		}
		assertNackPanics(&msg)
	})

	// registering a handler after the message is nacked should panic
	t.Run("nacked message", func(t *testing.T) {
		var msg Message
		// need to register a nack handler for message to be nacked
		msg.RegisterNackHandler(func(*Message, error) error { return nil })
		err := msg.Nack(nil)
		if err != nil {
			t.Fatalf("ack did not expect error, got %v", err)
		}
		assertAckPanics(&msg)
	})
}

func TestMessage_RegisterHandlerFail(t *testing.T) {
	assertRegisterAckHandlerPanics := func(msg *Message) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected msg.RegisterAckHandler to panic")
			}
		}()
		msg.RegisterAckHandler(func(*Message) error { return nil })
	}
	assertRegisterNackHandlerPanics := func(msg *Message) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected msg.RegisterNackHandler to panic")
			}
		}()
		msg.RegisterNackHandler(func(*Message, error) error { return nil })
	}

	// registering a handler after the message is acked should panic
	t.Run("acked message", func(t *testing.T) {
		var msg Message
		err := msg.Ack()
		if err != nil {
			t.Fatalf("ack did not expect error, got %v", err)
		}
		assertRegisterAckHandlerPanics(&msg)
		assertRegisterNackHandlerPanics(&msg)
	})

	// registering a handler after the message is nacked should panic
	t.Run("nacked message", func(t *testing.T) {
		var msg Message
		// need to register a nack handler for message to be nacked
		msg.RegisterNackHandler(func(*Message, error) error { return nil })
		err := msg.Nack(nil)
		if err != nil {
			t.Fatalf("ack did not expect error, got %v", err)
		}
		assertRegisterAckHandlerPanics(&msg)
		assertRegisterNackHandlerPanics(&msg)
	})
}

func assertMessageIsAcked(t *testing.T, msg *Message) {
	assert.Equal(t, MessageStatusAcked, msg.Status())
}

func assertMessageIsNacked(t *testing.T, msg *Message) {
	assert.Equal(t, MessageStatusNacked, msg.Status())
}

func assertMessageIsOpen(t *testing.T, msg *Message) {
	assert.Equal(t, MessageStatusOpen, msg.Status())
}
