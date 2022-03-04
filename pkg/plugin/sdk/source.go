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
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/jpillora/backoff"
	"gopkg.in/tomb.v2"
)

// Source fetches records from 3rd party resources and sends them to Conduit.
// All implementations must embed UnimplementedSource for forward compatibility.
type Source interface {
	// Configure is the first function to be called in a plugin. It provides the
	// plugin with the configuration that needs to be validated and stored. In
	// case the configuration is not valid it should return an error.
	Configure(context.Context, map[string]string) error

	// Open is called after Configure to signal the plugin it can prepare to
	// start producing records. If needed, the plugin should open connections in
	// this function. The context passed to Open will be cancelled once the
	// plugin receives a stop signal from Conduit.
	Open(context.Context, Position) error

	// Read returns a new Record and is supposed to block until there is either
	// a new record or the context gets cancelled. It can also return the error
	// ErrBackoffRetry to signal to the SDK it should call Read again with a
	// backoff retry.
	// If Read receives a cancelled context or the context is cancelled while
	// Read is running it must stop retrieving new records from the source
	// system and start returning records that have already been buffered. If
	// there are no buffered records left Read must return the context error to
	// signal a graceful stop. If Read returns ErrBackoffRetry while the context
	// is cancelled it will also signal that there are no records left and Read
	// won't be called again.
	// After Read returns an error the function won't be called again (except if
	// the error is ErrBackoffRetry, as mentioned above).
	// Read can be called concurrently with Ack.
	Read(context.Context) (Record, error)
	// Ack signals to the implementation that the record with the supplied
	// position was successfully processed. This method might be called after
	// the context of Read is already cancelled, since there might be
	// outstanding acks that need to be delivered. When Teardown is called it is
	// guaranteed there won't be any more calls to Ack.
	// Ack can be called concurrently with Read.
	Ack(context.Context, Position) error

	// Teardown signals to the plugin that there will be no more calls to any
	// other function. After Teardown returns, the plugin should be ready for a
	// graceful shutdown.
	Teardown(context.Context) error

	mustEmbedUnimplementedSource()
}

func NewSourcePlugin(impl Source) cpluginv1.SourcePlugin {
	if impl == nil {
		// prevent nil pointers
		impl = UnimplementedSource{}
	}
	return &sourcePluginAdapter{impl: impl}
}

type sourcePluginAdapter struct {
	impl Source

	// openAcks tracks how many acks we are still waiting for.
	openAcks int32
	// readDone will be closed after runRead stops running.
	readDone chan struct{}

	openCancel context.CancelFunc
	readCancel context.CancelFunc
	ackCancel  context.CancelFunc
	t          *tomb.Tomb
}

func (a *sourcePluginAdapter) Configure(ctx context.Context, req cpluginv1.SourceConfigureRequest) (cpluginv1.SourceConfigureResponse, error) {
	err := a.impl.Configure(ctx, req.Config)
	return cpluginv1.SourceConfigureResponse{}, err
}

func (a *sourcePluginAdapter) Start(ctx context.Context, req cpluginv1.SourceStartRequest) (cpluginv1.SourceStartResponse, error) {
	ctx, a.openCancel = context.WithCancel(ctx)
	err := a.impl.Open(ctx, req.Position)
	return cpluginv1.SourceStartResponse{}, err
}

func (a *sourcePluginAdapter) Run(ctx context.Context, stream cpluginv1.SourceRunStream) error {
	t, ctx := tomb.WithContext(ctx)
	readCtx, readCancel := context.WithCancel(ctx)
	ackCtx, ackCancel := context.WithCancel(ctx)

	a.t = t
	a.readCancel = readCancel
	a.ackCancel = ackCancel
	a.readDone = make(chan struct{})

	t.Go(func() error {
		defer close(a.readDone)
		return a.runRead(readCtx, stream)
	})
	t.Go(func() error {
		return a.runAck(ackCtx, stream)
	})

	<-t.Dying() // stop as soon as it's dying
	return t.Err()
}

func (a *sourcePluginAdapter) runRead(ctx context.Context, stream cpluginv1.SourceRunStream) error {
	// TODO make backoff params configurable (https://github.com/ConduitIO/conduit/issues/184)
	b := &backoff.Backoff{
		Factor: 2,
		Min:    time.Millisecond * 100,
		Max:    time.Second * 5,
	}

	for {
		r, err := a.impl.Read(ctx)
		if err != nil {
			if cerrors.Is(err, context.Canceled) {
				return nil // not an actual error
			}
			if cerrors.Is(err, ErrBackoffRetry) {
				// the plugin wants us to retry reading later
				select {
				case <-ctx.Done():
					// the plugin is using the SDK for long polling and relying
					// on the SDK to check for a cancelled context
					return nil
				case <-time.After(b.Duration()):
					continue
				}
			}
			return cerrors.Errorf("read plugin error: %w", err)
		}

		err = stream.Send(cpluginv1.SourceRunResponse{Record: a.convertRecord(r)})
		if err != nil {
			return cerrors.Errorf("read stream error: %w", err)
		}
		atomic.AddInt32(&a.openAcks, 1)

		// reset backoff retry
		b.Reset()
	}
}

func (a *sourcePluginAdapter) runAck(ctx context.Context, stream cpluginv1.SourceRunStream) error {
	type streamResponse struct {
		req cpluginv1.SourceRunRequest
		err error
	}

	out := make(chan streamResponse)
	// start fetching acks asynchronously and send them to the out channel
	go func() {
		defer close(out)
		for {
			req, err := stream.Recv()
			out <- streamResponse{req, err}
			if err != nil {
				return // stream is closed
			}
		}
	}()

	for {
		select {
		case resp := <-out:
			req, err := resp.req, resp.err
			if err != nil {
				if err == io.EOF {
					return nil // stream is closed, not an error
				}
				return cerrors.Errorf("ack stream error: %w", err)
			}
			err = a.impl.Ack(ctx, req.AckPosition)
			if err != nil {
				return cerrors.Errorf("ack plugin error: %w", err)
			}

			// decrease number of open acks
			atomic.AddInt32(&a.openAcks, -1)

			// check if we are still producing new records
			select {
			case <-a.readDone:
				// read function stopped running, check if we can stop too
				if atomic.LoadInt32(&a.openAcks) == 0 {
					a.ackCancel()
				}
			default:
				// continue
			}
		case <-ctx.Done():
			// context is cancelled when there are no more acks to be received

			// once the function returns the stream will return one more EOF error,
			// we will drain the channel to prevent a goroutine leak
			go func() { <-out }()
			return nil
		}
	}
}

func (a *sourcePluginAdapter) Stop(ctx context.Context, req cpluginv1.SourceStopRequest) (cpluginv1.SourceStopResponse, error) {
	// stop reading new messages
	a.openCancel()
	a.readCancel()
	<-a.readDone // wait for read to actually stop running
	if atomic.LoadInt32(&a.openAcks) == 0 {
		// we aren't waiting for any further acks, let's stop the ack goroutine as well
		a.ackCancel()
	}
	return cpluginv1.SourceStopResponse{}, nil
}

func (a *sourcePluginAdapter) Teardown(ctx context.Context, req cpluginv1.SourceTeardownRequest) (cpluginv1.SourceTeardownResponse, error) {
	// TODO add a timeout and kill plugin forcefully if needed (https://github.com/ConduitIO/conduit/issues/185)
	if a.t != nil {
		_ = a.t.Wait() // wait for Run to stop running
	}
	err := a.impl.Teardown(ctx)
	return cpluginv1.SourceTeardownResponse{}, err
}

func (a *sourcePluginAdapter) convertRecord(r Record) cpluginv1.Record {
	return cpluginv1.Record{
		Position:  r.Position,
		Metadata:  r.Metadata,
		Key:       a.convertData(r.Key),
		Payload:   a.convertData(r.Payload),
		CreatedAt: r.CreatedAt,
	}
}

func (a *sourcePluginAdapter) convertData(d Data) cpluginv1.Data {
	if d == nil {
		return nil
	}

	switch v := d.(type) {
	case RawData:
		return cpluginv1.RawData(v)
	case StructuredData:
		return cpluginv1.StructuredData(v)
	default:
		panic("unknown data type")
	}
}
