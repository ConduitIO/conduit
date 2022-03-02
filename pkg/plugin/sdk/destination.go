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

package sdk

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Destination receives records from Conduit and writes them to 3rd party
// resources.
// All implementations must embed UnimplementedDestination for forward
// compatibility.
// When implementing Destination you can choose between implementing the method
// WriteAsync or Write. If both are implemented, then WriteAsync will be used.
// Use WriteAsync if the plugin will cache records and write them to the 3rd
// party resource in batches, otherwise use Write.
type Destination interface {
	// Configure is the first function to be called in a plugin. It provides the
	// plugin with the configuration that needs to be validated and stored. In
	// case the configuration is not valid it should return an error.
	Configure(context.Context, map[string]string) error

	// Open is called after Configure to signal the plugin it can prepare to
	// start writing records. If needed, the plugin should open connections in
	// this function.
	Open(context.Context) error

	// WriteAsync receives a Record and can cache it internally and write it at
	// a later time. Once the record is successfully written to the destination
	// the plugin must call the provided AckFunc function with a nil error. If
	// the plugin failed to write the record to the destination it must call the
	// supplied AckFunc with a non-nil error. If any AckFunc is left uncalled
	// the connector will not be able to exit gracefully. The WriteAsync
	// function should only return an error in case a critical error happened,
	// it is expected that all future calls to WriteAsync will fail with the
	// same error.
	// If the plugin caches records before writing them to the destination it
	// needs to store the AckFunc as well and call it once the record is
	// written.
	// AckFunc will panic if it's called more than once.
	// If WriteAsync returns ErrUnimplemented the SDK will fall back and call
	// Write instead.
	WriteAsync(context.Context, Record, AckFunc) error

	// Write receives a Record and is supposed to write the record to the
	// destination right away. If the function returns nil the record is assumed
	// to have reached the destination.
	// WriteAsync takes precedence and will be tried first to write a record. If
	// WriteAsync is not implemented the SDK will fall back and use Write
	// instead.
	Write(context.Context, Record) error

	// Flush signals the plugin it should flush any cached records and call all
	// outstanding AckFunc functions. No more calls to Write will be issued
	// after Flush is called.
	Flush(context.Context) error

	// Teardown signals to the plugin that all records were written and there
	// will be no more calls to any other function. After Teardown returns, the
	// plugin should be ready for a graceful shutdown.
	Teardown(context.Context) error

	mustEmbedUnimplementedDestination()
}

type AckFunc func(error) error

func NewDestinationPlugin(impl Destination) cpluginv1.DestinationPlugin {
	if impl == nil {
		// prevent nil pointers
		impl = UnimplementedDestination{}
	}
	return &destinationPluginAdapter{impl: impl}
}

type destinationPluginAdapter struct {
	impl       Destination
	wgAckFuncs sync.WaitGroup
}

func (a *destinationPluginAdapter) Configure(ctx context.Context, req cpluginv1.DestinationConfigureRequest) (cpluginv1.DestinationConfigureResponse, error) {
	err := a.impl.Configure(ctx, req.Config)
	return cpluginv1.DestinationConfigureResponse{}, err
}

func (a *destinationPluginAdapter) Start(ctx context.Context, req cpluginv1.DestinationStartRequest) (cpluginv1.DestinationStartResponse, error) {
	err := a.impl.Open(ctx)
	return cpluginv1.DestinationStartResponse{}, err
}

func (a *destinationPluginAdapter) Run(ctx context.Context, stream cpluginv1.DestinationRunStream) error {
	var writeFunc func(context.Context, Record, cpluginv1.DestinationRunStream) error
	// writeFunc will overwrite itself the first time it is called, depending
	// on what the destination supports. If it supports async writes it will be
	// replaced with writeAsync, if not it will be replaced with write.
	writeFunc = func(ctx context.Context, r Record, stream cpluginv1.DestinationRunStream) error {
		err := a.writeAsync(ctx, r, stream)
		if cerrors.Is(err, ErrUnimplemented) {
			// WriteAsync is not implemented, fallback to Write and overwrite
			// writeFunc, so we don't try WriteAsync again.
			writeFunc = a.write
			return a.write(ctx, r, stream)
		}
		// WriteAsync is implemented, overwrite writeFunc as we don't need the
		// fallback logic anymore
		writeFunc = a.writeAsync
		return err
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				// stream is closed
				// wait for all acks to be sent back to Conduit
				err = a.waitForAcks(ctx)
				if err != nil {
					return err
				}
				return nil
			}
			return cerrors.Errorf("write stream error: %w", err)
		}
		r := a.convertRecord(req.Record)

		a.wgAckFuncs.Add(1)
		err = writeFunc(ctx, r, stream)
		if err != nil {
			return err
		}
	}
}

// write will call the Write function and send the acknowledgment back to
// Conduit when it returns.
func (a *destinationPluginAdapter) write(ctx context.Context, r Record, stream cpluginv1.DestinationRunStream) error {
	err := a.impl.Write(ctx, r)
	return a.ackFunc(r, stream)(err)
}

// writeAsync will call the WriteAsync function and provide the ack function to
// the implementation without calling it.
func (a *destinationPluginAdapter) writeAsync(ctx context.Context, r Record, stream cpluginv1.DestinationRunStream) error {
	return a.impl.WriteAsync(ctx, r, a.ackFunc(r, stream))
}

// ackFunc creates an AckFunc that can be called to signal that the record was
// processed. The destination plugin adapter keeps track of how many AckFunc
// functions still need to be called, once an AckFunc returns it decrements the
// internal counter by one.
// It is allowed to call AckFunc only once, if it's called more than once it
// will panic.
func (a *destinationPluginAdapter) ackFunc(r Record, stream cpluginv1.DestinationRunStream) AckFunc {
	var isCalled int32
	return func(ackErr error) error {
		if !atomic.CompareAndSwapInt32(&isCalled, 0, 1) {
			panic("same ack func must not be called twice")
		}

		defer a.wgAckFuncs.Done()
		var ackErrStr string
		if ackErr != nil {
			ackErrStr = ackErr.Error()
		}
		err := stream.Send(cpluginv1.DestinationRunResponse{
			AckPosition: r.Position,
			Error:       ackErrStr,
		})
		if err != nil {
			return cerrors.Errorf("ack stream error: %w", err)
		}
		return nil
	}
}

func (a *destinationPluginAdapter) waitForAcks(ctx context.Context) error {
	// wait for all acks to be sent back to Conduit
	ackFuncsDone := make(chan struct{})
	go func() {
		a.wgAckFuncs.Wait()
		close(ackFuncsDone)
	}()

	select {
	case <-ackFuncsDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Minute): // TODO make the timeout configurable (https://github.com/ConduitIO/conduit/issues/183)
		return cerrors.New("stop timeout reached")
	}
}

func (a *destinationPluginAdapter) Stop(ctx context.Context, req cpluginv1.DestinationStopRequest) (cpluginv1.DestinationStopResponse, error) {
	// flush cached records
	err := a.impl.Flush(ctx)
	if err != nil {
		return cpluginv1.DestinationStopResponse{}, err
	}

	return cpluginv1.DestinationStopResponse{}, nil
}

func (a *destinationPluginAdapter) Teardown(ctx context.Context, req cpluginv1.DestinationTeardownRequest) (cpluginv1.DestinationTeardownResponse, error) {
	// wait for all acks to be sent back to Conduit
	err := a.waitForAcks(ctx)
	if err != nil {
		return cpluginv1.DestinationTeardownResponse{}, err
	}

	err = a.impl.Teardown(ctx)
	if err != nil {
		return cpluginv1.DestinationTeardownResponse{}, err
	}
	return cpluginv1.DestinationTeardownResponse{}, nil
}

func (a *destinationPluginAdapter) convertRecord(r cpluginv1.Record) Record {
	return Record{
		Position:  r.Position,
		Metadata:  r.Metadata,
		Key:       a.convertData(r.Key),
		Payload:   a.convertData(r.Payload),
		CreatedAt: r.CreatedAt,
	}
}

func (a *destinationPluginAdapter) convertData(d cpluginv1.Data) Data {
	if d == nil {
		return nil
	}

	switch v := d.(type) {
	case cpluginv1.RawData:
		return RawData(v)
	case cpluginv1.StructuredData:
		return StructuredData(v)
	default:
		panic("unknown data type")
	}
}
