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
	"context"
	"io"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

// SourceConfig controls a fake Source. The zero value is a source that produces
// unlimited one-record batches of StructuredShape records with no latency.
type SourceConfig struct {
	// ID is the connector ID and the prefix of generated positions.
	ID string
	// Total is the number of records to produce before the source runs dry; 0
	// or less means unlimited.
	Total int
	// BatchSize is the number of records per Read; 0 means 1. A final batch may
	// be smaller.
	BatchSize int
	// Shape builds the records; nil means StructuredShape.
	Shape Shape
	// ReadLatency is slept (real time) on every Read before it returns. Use it
	// in benchmarks; tests should use ReadGate or ReadHook.
	ReadLatency time.Duration
	// ReadHook, if set, runs at the start of every Read with the 1-based call
	// number. A non-nil error fails that Read; the hook may also block, which
	// is how a test stalls a Read at an exact point.
	ReadHook func(ctx context.Context, call int) error
	// EOFWhenDry makes a source that has produced Total records return io.EOF
	// (arch-v2 treats that as a graceful end of that source). The default is
	// to block until Stop, Teardown or ctx, like a real streaming source.
	EOFWhenDry bool
	// Discard stops the source retaining acked positions (Acked returns nil;
	// AckedCount and WaitAcked still work). Benchmarks set it so memory does not
	// grow with b.N.
	Discard bool
	// Ledger, if set, is told about every Ack so it can flag an ack of a record
	// that was not yet durable downstream.
	Ledger *Ledger
	// AckHook, if set, runs on every Ack before it is recorded. A non-nil error
	// is returned to the engine and the positions are not recorded.
	AckHook func(ctx context.Context, positions []opencdc.Position) error
}

// Source is a fake source connector. It satisfies the Source interfaces of both
// engines. All methods are safe for concurrent use.
type Source struct {
	cfg SourceConfig

	// ReadGate stalls Read while closed. It is part of the public surface so a
	// test can close it at any point.
	ReadGate Gate

	mu        sync.Mutex
	next      int // index of the next record to produce
	calls     int
	lastPos   opencdc.Position
	acked     []opencdc.Position
	opened    bool
	stopped   bool
	crashed   error
	torn      chan struct{}
	tornOnce  sync.Once
	errCh     chan error
	produced  counter
	ackedCnt  counter
	readCalls counter
}

// NewSource returns a fake source.
func NewSource(cfg SourceConfig) *Source {
	if cfg.ID == "" {
		cfg.ID = "src"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1
	}
	if cfg.Shape == nil {
		cfg.Shape = StructuredShape
	}
	return &Source{
		cfg:   cfg,
		torn:  make(chan struct{}),
		errCh: make(chan error, 1),
	}
}

// ID implements the engines' Source interfaces.
func (s *Source) ID() string { return s.cfg.ID }

// Open implements the engines' Source interfaces. Opening twice is an error,
// like a real connector ("another instance is already running"), so a test
// catches an engine that double-opens a shared connector.
func (s *Source) Open(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened {
		return ErrAlreadyOpen
	}
	s.opened = true
	return nil
}

// Errors implements the engines' Source interfaces. A value is sent by Crash.
func (s *Source) Errors() <-chan error { return s.errCh }

// Read implements the engines' Source interfaces.
func (s *Source) Read(ctx context.Context) ([]opencdc.Record, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	s.readCalls.add(1)

	if err := s.ReadGate.Wait(ctx, s.torn); err != nil {
		return nil, s.readErr(err)
	}
	if err := sleep(ctx, s.cfg.ReadLatency, s.torn); err != nil {
		return nil, s.readErr(err)
	}
	if s.cfg.ReadHook != nil {
		hctx, cancel := withDone(ctx, s.torn)
		err := s.cfg.ReadHook(hctx, call)
		cancel()
		if err != nil {
			if s.isTorn() {
				return nil, s.closedErr()
			}
			return nil, err
		}
	}

	s.mu.Lock()
	if s.crashed != nil {
		err := s.crashed
		s.mu.Unlock()
		return nil, err
	}
	if s.isTorn() {
		s.mu.Unlock()
		return nil, connectorPlugin.ErrStreamNotOpen
	}
	stopped := s.stopped
	dry := stopped || (s.cfg.Total > 0 && s.next >= s.cfg.Total)
	if dry {
		s.mu.Unlock()
		if s.cfg.EOFWhenDry && !stopped {
			return nil, io.EOF
		}
		// Block like a real stream with nothing to deliver, until the engine
		// tears the connector down or gives up on the context.
		select {
		case <-s.torn:
			return nil, s.closedErr()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	n := s.cfg.BatchSize
	if s.cfg.Total > 0 && s.cfg.Total-s.next < n {
		n = s.cfg.Total - s.next
	}
	recs := make([]opencdc.Record, n)
	for i := range recs {
		recs[i] = s.cfg.Shape(s.cfg.ID, s.next+i)
	}
	s.next += n
	s.lastPos = recs[n-1].Position
	s.mu.Unlock()

	s.produced.add(n)
	return recs, nil
}

func (s *Source) isTorn() bool {
	select {
	case <-s.torn:
		return true
	default:
		return false
	}
}

func (s *Source) readErr(err error) error {
	if cerrorsIs(err, errGateAbandoned) {
		return s.closedErr()
	}
	return err
}

// closedErr is what a Read released by teardown returns: the crash error if the
// connector crashed, otherwise the closed-stream sentinel.
func (s *Source) closedErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crashed != nil {
		return s.crashed
	}
	return connectorPlugin.ErrStreamNotOpen
}

// Ack implements the engines' Source interfaces.
func (s *Source) Ack(ctx context.Context, positions []opencdc.Position) error {
	if s.cfg.Ledger != nil {
		// Invariant 1 check point: runs before the ack is recorded, at the
		// moment the engine reports the records as handled.
		s.cfg.Ledger.checkAck(s.cfg.ID, positions)
	}
	if s.cfg.AckHook != nil {
		if err := s.cfg.AckHook(ctx, positions); err != nil {
			return err
		}
	}
	if !s.cfg.Discard {
		s.mu.Lock()
		s.acked = append(s.acked, positions...)
		s.mu.Unlock()
	}
	s.ackedCnt.add(len(positions))
	return nil
}

// Stop implements the arch v1 Source interface. It returns the position of the
// last record produced and stops producing, like a real connector's Stop.
func (s *Source) Stop(context.Context) (opencdc.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	return s.lastPos, nil
}

// Teardown implements the engines' Source interfaces. It releases any Read that
// is blocked or stalled.
func (s *Source) Teardown(context.Context) error {
	s.tornOnce.Do(func() { close(s.torn) })
	return nil
}

// Crash simulates the plugin dying: pending and future Reads fail with err
// (ErrCrashed when nil) and err is delivered on Errors().
func (s *Source) Crash(err error) {
	if err == nil {
		err = ErrCrashed
	}
	s.mu.Lock()
	s.crashed = err
	s.mu.Unlock()
	select {
	case s.errCh <- err:
	default:
	}
	// Release a Read stuck in the gate or dry-wait so it observes the crash.
	s.tornOnce.Do(func() { close(s.torn) })
}

// Produced is the number of records handed to the engine so far.
func (s *Source) Produced() int { return s.produced.load() }

// ReadCalls is the number of Read calls so far, including blocked ones.
func (s *Source) ReadCalls() int { return s.readCalls.load() }

// Acked returns the positions acked so far, in the order the engine acked them.
func (s *Source) Acked() []opencdc.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]opencdc.Position(nil), s.acked...)
}

// AckedCount is len(Acked()) without the copy.
func (s *Source) AckedCount() int { return s.ackedCnt.load() }

// WaitAcked blocks until at least n positions were acked or ctx ends.
func (s *Source) WaitAcked(ctx context.Context, n int) error { return s.ackedCnt.waitAtLeast(ctx, n) }

// WaitProduced blocks until at least n records were produced or ctx ends.
func (s *Source) WaitProduced(ctx context.Context, n int) error {
	return s.produced.waitAtLeast(ctx, n)
}

// WaitReadCalls blocks until Read was called at least n times or ctx ends. A
// test uses it to know a Read is parked in a stall.
func (s *Source) WaitReadCalls(ctx context.Context, n int) error {
	return s.readCalls.waitAtLeast(ctx, n)
}

// TornDown reports whether Teardown was called (or the source crashed).
func (s *Source) TornDown() bool { return s.isTorn() }
