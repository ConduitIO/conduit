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
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestInspector_Send_NoSessions(*testing.T) {
	underTest := New(log.Nop(), 10)
	underTest.Send(context.Background(), []opencdc.Record{{}})
}

func TestInspector_Send_SingleSession(t *testing.T) {
	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background(), "test-id")

	r := opencdc.Record{
		Position: opencdc.Position("test-pos"),
	}
	underTest.Send(context.Background(), []opencdc.Record{r})
	assertGotRecord(is.New(t), s, r)
}

func TestInspector_Send_MultipleSessions(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s1 := underTest.NewSession(context.Background(), "test-id")
	s2 := underTest.NewSession(context.Background(), "test-id")

	r := opencdc.Record{
		Position: opencdc.Position("test-pos"),
	}
	underTest.Send(context.Background(), []opencdc.Record{r})
	assertGotRecord(is, s1, r)
	assertGotRecord(is, s2, r)
}

func TestInspector_Send_SessionClosed(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background(), "test-id")

	r := opencdc.Record{
		Position: opencdc.Position("test-pos"),
	}
	underTest.Send(context.Background(), []opencdc.Record{r})
	assertGotRecord(is, s, r)

	underTest.remove(s.id)
	underTest.Send(
		context.Background(),
		[]opencdc.Record{
			{Position: opencdc.Position("test-pos-2")},
		},
	)
}

func TestInspector_Close(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background(), "test-id")

	underTest.Close()
	_, ok := <-s.C
	is.True(!ok)
}

func TestInspector_Send_SessionCtxCanceled(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	ctx, cancel := context.WithCancel(context.Background())
	s := underTest.NewSession(ctx, "test-id")

	r := opencdc.Record{
		Position: opencdc.Position("test-pos"),
	}
	underTest.Send(context.Background(), []opencdc.Record{r})
	assertGotRecord(is, s, r)

	cancel()

	_, got, err := cchan.ChanOut[opencdc.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.NoErr(err)
	is.True(!got) // expected no record
}

func TestInspector_Send_SlowConsumer(t *testing.T) {
	// When a session's buffer is full, then further records will be dropped.
	// In this test we set up an inspector with a buffer size of 10.
	// Then we send 11 records (without reading them immediately).
	// The expected behavior is that we will be able to get the first 10,
	// and that the last record never arrives.
	is := is.New(t)

	bufferSize := 10
	underTest := New(log.Nop(), bufferSize)
	s := underTest.NewSession(context.Background(), "test-id")

	for i := 0; i < bufferSize+1; i++ {
		underTest.Send(
			context.Background(),
			[]opencdc.Record{
				{Position: opencdc.Position(fmt.Sprintf("test-pos-%v", i))},
			},
		)
	}

	for i := 0; i < bufferSize; i++ {
		assertGotRecord(
			is,
			s,
			opencdc.Record{
				Position: opencdc.Position(fmt.Sprintf("test-pos-%v", i)),
			},
		)
	}

	_, got, err := cchan.ChanOut[opencdc.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
	is.True(!got)

	is.Equal(s.Dropped(), int64(1)) // exactly the one record past the buffer was dropped
}

// TestInspector_Dropped_PerSession proves drops are counted per session, not per
// Send: with two sessions on the same inspector, only the never-drained session
// drops records; the one drained after every Send never fills, so it drops
// nothing. Draining synchronously (not via a background goroutine) keeps the test
// deterministic — no scheduling race that could make the "fast" session drop.
func TestInspector_Dropped_PerSession(t *testing.T) {
	is := is.New(t)

	bufferSize := 5
	underTest := New(log.Nop(), bufferSize)
	slow := underTest.NewSession(context.Background(), "test-id") // never drained
	fast := underTest.NewSession(context.Background(), "test-id") // drained each Send

	const sent = 20 // well past bufferSize
	for i := 0; i < sent; i++ {
		underTest.Send(context.Background(), []opencdc.Record{
			{Position: opencdc.Position(fmt.Sprintf("test-pos-%v", i))},
		})
		<-fast.C // keep fast empty so it never drops
	}

	is.Equal(slow.Dropped(), int64(sent-bufferSize)) // slow kept only bufferSize, dropped the rest
	is.Equal(fast.Dropped(), int64(0))               // fast was drained, dropped nothing
}

// TestInspector_Dropped_NoDropWhenRoom proves a session with buffer room drops
// nothing.
func TestInspector_Dropped_NoDropWhenRoom(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background(), "test-id")

	for i := 0; i < 5; i++ { // fewer than the buffer size
		underTest.Send(context.Background(), []opencdc.Record{
			{Position: opencdc.Position(fmt.Sprintf("test-pos-%v", i))},
		})
	}
	is.Equal(s.Dropped(), int64(0))
}

func assertGotRecord(is *is.I, s *Session, recWant opencdc.Record) {
	recGot, got, err := cchan.ChanOut[opencdc.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.NoErr(err)
	is.True(got)
	is.Equal(recWant, recGot)
}
