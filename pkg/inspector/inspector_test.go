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

	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestInspector_Send_NoSessions(t *testing.T) {
	underTest := New(log.Nop(), 10)
	underTest.Send(context.Background(), record.Record{})
}

func TestInspector_Send_SingleSession(t *testing.T) {
	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background())

	r := record.Record{
		Position: record.Position("test-pos"),
	}
	underTest.Send(context.Background(), r)
	assertGotRecord(is.New(t), s, r)
}

func TestInspector_Send_MultipleSessions(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s1 := underTest.NewSession(context.Background())
	s2 := underTest.NewSession(context.Background())

	r := record.Record{
		Position: record.Position("test-pos"),
	}
	underTest.Send(context.Background(), r)
	assertGotRecord(is, s1, r)
	assertGotRecord(is, s2, r)
}

func TestInspector_Send_SessionClosed(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	s := underTest.NewSession(context.Background())

	r := record.Record{
		Position: record.Position("test-pos"),
	}
	underTest.Send(context.Background(), r)
	assertGotRecord(is, s, r)

	s.close(nil)
	underTest.Send(
		context.Background(),
		record.Record{
			Position: record.Position("test-pos-2"),
		},
	)
}

func TestInspector_Send_SessionCtxCanceled(t *testing.T) {
	is := is.New(t)

	underTest := New(log.Nop(), 10)
	ctx, cancel := context.WithCancel(context.Background())
	s := underTest.NewSession(ctx)

	r := record.Record{
		Position: record.Position("test-pos"),
	}
	underTest.Send(context.Background(), r)
	assertGotRecord(is, s, r)

	cancel()

	_, got, err := cchan.Chan[record.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
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
	s := underTest.NewSession(context.Background())

	for i := 0; i < bufferSize+1; i++ {
		s.send(
			context.Background(),
			record.Record{
				Position: record.Position(fmt.Sprintf("test-pos-%v", i)),
			},
		)
	}

	for i := 0; i < bufferSize; i++ {
		assertGotRecord(
			is,
			s,
			record.Record{
				Position: record.Position(fmt.Sprintf("test-pos-%v", i)),
			},
		)
	}

	_, got, err := cchan.Chan[record.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.True(cerrors.Is(err, context.DeadlineExceeded))
	is.True(!got)
}

func assertGotRecord(is *is.I, s *Session, recWant record.Record) {
	recGot, got, err := cchan.Chan[record.Record](s.C).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.NoErr(err)
	is.True(got)
	is.Equal(recWant, recGot)
}
