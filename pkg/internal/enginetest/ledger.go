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

package enginetest

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

var (
	// errGateAbandoned is returned by a stalled fake call whose connector was
	// torn down while it waited.
	errGateAbandoned = cerrors.New("enginetest: connector torn down while stalled")
	// ErrCrashed is the default error a crashed fake reports.
	ErrCrashed = cerrors.New("enginetest: simulated plugin crash")
	// ErrAlreadyOpen is returned when a fake connector is opened twice.
	ErrAlreadyOpen = cerrors.New("enginetest: connector is already open")
)

func cerrorsIs(err, target error) bool { return cerrors.Is(err, target) }

// Shape builds record i of source srcID. Positions must be unique per source;
// the presets below use "<srcID>/<i>".
type Shape func(srcID string, i int) opencdc.Record

// StructuredShape is a small structured record, the default.
func StructuredShape(srcID string, i int) opencdc.Record {
	return opencdc.Record{
		Position:  Position(srcID, i),
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{opencdc.MetadataConduitSourceConnectorID: srcID},
		Key:       opencdc.RawData(strconv.Itoa(i)),
		Payload: opencdc.Change{
			After: opencdc.StructuredData{"id": i, "name": "enginetest-record"},
		},
	}
}

// RawShape returns a Shape producing raw payloads of size bytes.
func RawShape(size int) Shape {
	body := make([]byte, size)
	return func(srcID string, i int) opencdc.Record {
		return opencdc.Record{
			Position:  Position(srcID, i),
			Operation: opencdc.OperationCreate,
			Metadata:  opencdc.Metadata{opencdc.MetadataConduitSourceConnectorID: srcID},
			Key:       opencdc.RawData(strconv.Itoa(i)),
			Payload:   opencdc.Change{After: opencdc.RawData(body)},
		}
	}
}

// Position is the position the preset shapes give record i of srcID.
func Position(srcID string, i int) opencdc.Position {
	// One allocation: this runs once per record, including inside benchmarks.
	b := make([]byte, 0, len(srcID)+1+8)
	b = append(b, srcID...)
	b = append(b, '/')
	return opencdc.Position(strconv.AppendInt(b, int64(i), 10))
}

// Ledger records, at the moment a source is acked, whether each acked record
// was already durable downstream. It is how a test asserts invariant 1 (never
// acknowledge a record upstream before it is durably handled downstream) at the
// offending Ack call instead of after the fact.
//
// A record is durable when the destination handed its (non-error) ack to the
// engine in Ack, i.e. the moment the engine could know. Flushing inside Write
// is deliberately not enough: an engine that acks the source after Write
// without waiting for the destination's ack must be caught. A record is also
// durable when it reached the DLQ destination (a nacked record that the DLQ
// accepted is durably handled).
// Share one Ledger between the Source (SourceConfig.Ledger) and every
// Destination (DestinationConfig.Ledger), and name the destinations a record
// must reach with Require.
type Ledger struct {
	mu         sync.Mutex
	required   []string
	dlq        string
	durable    map[string]map[string]struct{} // destination ID -> position
	violations []string
	failed     chan struct{} // closed on the first violation
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{
		durable: make(map[string]map[string]struct{}),
		failed:  make(chan struct{}),
	}
}

// Require names the destinations a record must be durable in before its source
// may be acked. The DLQ destination, if set, satisfies any of them.
func (l *Ledger) Require(destinationIDs ...string) *Ledger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.required = append(l.required, destinationIDs...)
	return l
}

// DLQ names the dead-letter destination.
func (l *Ledger) DLQ(destinationID string) *Ledger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dlq = destinationID
	return l
}

func (l *Ledger) markDurable(destinationID string, pos opencdc.Position) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.durable[destinationID]
	if m == nil {
		m = make(map[string]struct{})
		l.durable[destinationID] = m
	}
	m[string(pos)] = struct{}{}
}

func (l *Ledger) checkAck(sourceID string, positions []opencdc.Position) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.required) == 0 {
		// A ledger that requires nothing checks nothing. Fail loudly instead
		// of reporting a vacuous pass.
		l.addViolationLocked("ledger has no Require() destinations: it would pass every ack; call Require")
		return
	}
	for _, pos := range positions {
		for _, dest := range l.required {
			if _, ok := l.durable[dest][string(pos)]; ok {
				continue
			}
			if _, ok := l.durable[l.dlq][string(pos)]; l.dlq != "" && ok {
				continue
			}
			l.addViolationLocked(fmt.Sprintf(
				"source %s acked position %q before it was durable in destination %s", sourceID, pos, dest,
			))
		}
	}
}

// Failed returns a channel closed at the first violation, so a test can stop
// waiting for a broken engine instead of running into its timeout.
func (l *Ledger) Failed() <-chan struct{} { return l.failed }

func (l *Ledger) addViolationLocked(v string) {
	if len(l.violations) == 0 {
		close(l.failed)
	}
	l.violations = append(l.violations, v)
}

// Violations returns every ack-before-durable event seen so far. An empty
// result means invariant 1 held for the run.
func (l *Ledger) Violations() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.violations...)
}
