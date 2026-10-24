// Copyright © 2026 Meroxa, Inc.
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

package funnel

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// A source position can embed record key values, so internal-invariant errors
// name positions by salted hash only. Each test fails if the raw position is
// formatted into the message.

const secretPos = "customer-SECRET-4711"

func assertHashOnly(is *is.I, msg string, pos opencdc.Position) {
	is.Helper()
	is.True(strings.Contains(msg, log.KeyHash(pos)))
	is.True(!strings.Contains(msg, secretPos))
}

func TestMultiAckNacker_DuplicatePositions_ErrorHasHashOnly(t *testing.T) {
	is := is.New(t)
	pos := opencdc.Position(secretPos)
	_, err := newMultiAckNacker(&fakeParentAckNacker{}, 2, []opencdc.Position{pos, pos})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	assertHashOnly(is, ce.Message, pos)
}

func TestMultiAckNacker_UnknownPosition_ErrorHasHashOnly(t *testing.T) {
	is := is.New(t)
	m, err := newMultiAckNacker(&fakeParentAckNacker{}, 2, []opencdc.Position{opencdc.Position("known")})
	is.NoErr(err)

	pos := opencdc.Position(secretPos)
	_, err = m.indexOf(pos)
	is.True(err != nil)
	assertHashOnly(is, err.Error(), pos)
}

func TestRunAckNacker_VoteAfterRelease_ErrorHasHashOnly(t *testing.T) {
	is := is.New(t)
	pos := opencdc.Position(secretPos)
	run := &splitRun{origPos: pos, total: 1, released: true}
	batch := NewBatch([]opencdc.Record{{Position: pos}})
	batch.runs = []*splitRun{run}

	err := newRunAckNacker(&fakeParentAckNacker{}).Ack(context.Background(), batch)
	is.True(err != nil)
	assertHashOnly(is, err.Error(), pos)
}

func TestSplitRun_DoubleCredit_ErrorHasHashOnly(t *testing.T) {
	is := is.New(t)
	pos := opencdc.Position(secretPos)
	run := &splitRun{origPos: pos, total: 1, terminalCount: 2}

	_, err := run.complete()
	is.True(err != nil)
	assertHashOnly(is, err.Error(), pos)
}
