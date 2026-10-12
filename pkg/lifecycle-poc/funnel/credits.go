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
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
)

// Read-ahead window defaults, per source. These are vars, not consts, only so
// tests and the prototype's measurements can vary them; production code must
// not reassign them.
//
// The window is expressed in records and estimated bytes, not batches: a
// default source returns one record per read, so a window counted in batches
// would be a window of a handful of records.
var (
	// defaultCreditRecords is the maximum number of records a source may have
	// read but not yet released (Source.Ack returned). It is also the bound on
	// duplicates after a crash, together with the persister's flush lag.
	defaultCreditRecords int64 = 4000
	// defaultCreditBytes is the byte guard rail, estimated cheaply (see
	// estimateRecordSize), not exact.
	defaultCreditBytes int64 = 64 << 20
)

// credits bounds how much a single source may have in flight: read from the
// source, not yet released by the ack coordinator. It is the engine's
// backpressure: when a destination stalls, credits run out, the reader stops
// calling Read and memory stays flat.
//
// The reader waits for free credits BEFORE reading (wait) and charges what it
// actually got AFTER (charge), so memory overshoots the cap by at most one
// read response. A response larger than the whole window is admitted when
// nothing else is in flight, otherwise it could never run.
type credits struct {
	maxRecords int64
	maxBytes   int64

	mu      sync.Mutex
	records int64
	bytes   int64
	changed chan struct{} // closed and replaced on every release
}

func newCredits(maxRecords, maxBytes int64) *credits {
	return &credits{
		maxRecords: maxRecords,
		maxBytes:   maxBytes,
		changed:    make(chan struct{}),
	}
}

// wait blocks until there is room for at least one more record, ctx is done or
// stop is closed. It returns false if it gave up.
func (c *credits) wait(ctx context.Context, stop <-chan struct{}) bool {
	for {
		c.mu.Lock()
		if c.records == 0 || (c.records < c.maxRecords && c.bytes < c.maxBytes) {
			c.mu.Unlock()
			return true
		}
		ch := c.changed
		c.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return false
		case <-stop:
			return false
		}
	}
}

func (c *credits) charge(records, bytes int64) {
	c.mu.Lock()
	c.records += records
	c.bytes += bytes
	c.mu.Unlock()
}

func (c *credits) release(records, bytes int64) {
	c.mu.Lock()
	c.records -= records
	c.bytes -= bytes
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

// inFlight returns the records and bytes currently charged.
func (c *credits) inFlight() (records, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.records, c.bytes
}

// estimateRecordSize is a cheap, conservative-enough size estimate. It never
// marshals: raw payloads count their length, structured payloads a bounded
// shallow estimate. It reads the record and never mutates it. The record
// count cap is the hard bound; this is a guard rail for large payloads.
func estimateRecordSize(r opencdc.Record) int64 {
	n := int64(len(r.Position)) + 64
	for k, v := range r.Metadata {
		n += int64(len(k) + len(v))
	}
	n += dataSize(r.Key)
	n += dataSize(r.Payload.Before)
	n += dataSize(r.Payload.After)
	return n
}

func dataSize(d opencdc.Data) int64 {
	switch v := d.(type) {
	case nil:
		return 0
	case opencdc.RawData:
		return int64(len(v))
	case opencdc.StructuredData:
		// ~48 bytes per top-level field plus key length, strings counted.
		var n int64
		for k, val := range v {
			n += 48 + int64(len(k))
			if s, ok := val.(string); ok {
				n += int64(len(s))
			}
		}
		return n
	default:
		return 64
	}
}
