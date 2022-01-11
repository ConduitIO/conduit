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

package source

import (
	"github.com/conduitio/conduit/pkg/record"
)

// CDCIterator listens for events from the WAL and pushes them into its buffer.
// It iterates through that Buffer so that we have a controlled way to get 1
// record from our CDC buffer without having to expose a loop to the main Read.
type CDCIterator struct {
	buffer chan record.Record
	pos    int64
}

var _ Iterator = (*CDCIterator)(nil)

// NewIterator creates an iterator from a channel and sets its position to 0.
func NewIterator(rch chan record.Record) *CDCIterator {
	return &CDCIterator{
		buffer: rch,
		pos:    0,
	}
}

// HasNext returns true if there is an item in the buffer.
func (i *CDCIterator) HasNext() bool {
	return len(i.buffer) > 0
}

// Next returns the next record in the buffer. This is a blocking operation
// so it should only be called if we've checked that HasNext is true or else
// it will block until a record is inserted into the queue.
func (i *CDCIterator) Next() (record.Record, error) {
	r := <-i.buffer
	return r, nil
}

// Push appends a Record to the buffer.
func (i *CDCIterator) Push(r record.Record) {
	i.buffer <- r
}

// Teardown is a noop that returns nil since our buffer requires no cleanup
func (i *CDCIterator) Teardown() error {
	return nil
}
