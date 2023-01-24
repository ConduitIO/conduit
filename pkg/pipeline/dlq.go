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

package pipeline

import (
	"bytes"
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline/stream"
	"github.com/conduitio/conduit/pkg/record"
)

// DLQDestination is a DLQ handler that forwards DLQ records to a destination
// connector.
type DLQDestination struct {
	Destination stream.Destination
	Logger      log.CtxLogger

	lastPosition record.Position
}

func (d *DLQDestination) Open(ctx context.Context) error {
	return d.Destination.Open(ctx)
}

// Write writes the record synchronously to the destination, meaning that it
// waits until an ack is received for the record before it returns. If the
// record write fails or the destination returns a nack, the function returns an
// error.
func (d *DLQDestination) Write(ctx context.Context, rec record.Record) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg     csync.WaitGroup
		pos    record.Position
		ackErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		pos, ackErr = d.Destination.Ack(ctx)
	}()

	err := d.Destination.Write(ctx, rec)
	if err != nil {
		return err
	}

	d.lastPosition = rec.Position

	err = wg.Wait(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(rec.Position, pos) {
		return cerrors.Errorf("received unexpected ack, expected position %q but got %q", rec.Position, pos)
	}
	if ackErr != nil {
		return ackErr // nack
	}

	return nil
}

// Close stops the destination and tears it down.
func (d *DLQDestination) Close(ctx context.Context) (err error) {
	stopErr := d.Destination.Stop(ctx, d.lastPosition)
	if stopErr != nil {
		defer func() {
			if err == nil {
				// replace returned error with stop error
				err = stopErr
			}
		}()
		// log this error right away because we're not sure the connector
		// will be able to stop right away, we might block for 1 minute
		// waiting for acks and we don't want the log to be empty
		d.Logger.Err(ctx, stopErr).Msg("could not stop DLQ connector")
	}

	// teardown will kill the plugin process
	return d.Destination.Teardown(ctx)
}
