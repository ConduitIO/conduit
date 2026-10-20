// Copyright © 2024 Meroxa, Inc.
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

package builtin

import (
	"context"
	"io"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
)

type InMemoryDestinationRunStream struct {
	stream *inMemoryStream[pconnector.DestinationRunRequest, pconnector.DestinationRunResponse]
}

func (s *InMemoryDestinationRunStream) Init(ctx context.Context) {
	s.stream = &inMemoryStream[pconnector.DestinationRunRequest, pconnector.DestinationRunResponse]{
		ctx:      ctx,
		reqChan:  make(chan pconnector.DestinationRunRequest),
		respChan: make(chan pconnector.DestinationRunResponse),
		stopChan: make(chan struct{}),
		// The destination server's Send carries acks (small position bytes)
		// back to the engine. Clone them so the engine never shares ack bytes
		// with the destination plugin that produced them.
		cloneServerResponse: true,
	}
}

func (s *InMemoryDestinationRunStream) Client() pconnector.DestinationRunStreamClient {
	if s.stream == nil {
		panic("invalid use of builtin.InMemoryDestinationRunStream - stream has not been initialized using Init")
	}
	return (*inMemoryStreamClient[pconnector.DestinationRunRequest, pconnector.DestinationRunResponse])(s.stream)
}

func (s *InMemoryDestinationRunStream) Server() pconnector.DestinationRunStreamServer {
	if s.stream == nil {
		panic("invalid use of builtin.InMemoryDestinationRunStream - stream has not been initialized using Init")
	}
	return (*inMemoryStreamServer[pconnector.DestinationRunRequest, pconnector.DestinationRunResponse])(s.stream)
}

func (s *InMemoryDestinationRunStream) Close(reason error) bool {
	return s.stream.Close(reason)
}

type InMemorySourceRunStream struct {
	stream *inMemoryStream[pconnector.SourceRunRequest, pconnector.SourceRunResponse]
}

func (s *InMemorySourceRunStream) Init(ctx context.Context) {
	s.stream = &inMemoryStream[pconnector.SourceRunRequest, pconnector.SourceRunResponse]{
		ctx:      ctx,
		reqChan:  make(chan pconnector.SourceRunRequest),
		respChan: make(chan pconnector.SourceRunResponse),
		stopChan: make(chan struct{}),
		// The source server's Send carries records from the source plugin to
		// the engine. The SDK already transfers ownership of these records on
		// ReadN (see conduit-connector-sdk Source.ReadN: "once ReadN returns,
		// the returned records belong to the SDK... allocate a new position,
		// metadata map and data buffers for every record"), and after Send the
		// SDK only reads the last record's Position for its Stop response. The
		// engine never mutates a record's Position in place, so the source's
		// copy stays correct without cloning every record at this boundary —
		// which would double the record's memory (Position bytes + Metadata map
		// + Key and Payload StructuredData) on the hottest path in the process.
		// See the invariant comment on inMemoryStreamServer.Send.
		cloneServerResponse: false,
	}
}

func (s *InMemorySourceRunStream) Client() pconnector.SourceRunStreamClient {
	if s.stream == nil {
		panic("invalid use of builtin.InMemoryDestinationRunStream - stream has not been initialized using Init")
	}
	return (*inMemoryStreamClient[pconnector.SourceRunRequest, pconnector.SourceRunResponse])(s.stream)
}

func (s *InMemorySourceRunStream) Server() pconnector.SourceRunStreamServer {
	if s.stream == nil {
		panic("invalid use of builtin.InMemoryDestinationRunStream - stream has not been initialized using Init")
	}
	return (*inMemoryStreamServer[pconnector.SourceRunRequest, pconnector.SourceRunResponse])(s.stream)
}

func (s *InMemorySourceRunStream) Close(reason error) bool {
	return s.stream.Close(reason)
}

type inMemoryStream[REQ any, RES any] struct {
	ctx      context.Context //nolint:containedctx // We need to mimic the behavior of a gRPC stream
	reqChan  chan REQ
	respChan chan RES
	stopChan chan struct{}

	// cloneServerResponse controls whether inMemoryStreamServer.Send clones
	// the response before handing it to the client. It is false only for the
	// source stream, whose server hands ownership of each record to the engine
	// (see InMemorySourceRunStream.Init); it stays true everywhere else so the
	// client can never observe a plugin's data being mutated.
	cloneServerResponse bool

	reason error
	m      sync.Mutex
}

func (s *inMemoryStream[REQ, RES]) Close(reason error) bool {
	s.m.Lock()
	defer s.m.Unlock()
	select {
	case <-s.stopChan:
		// channel already closed
		return false
	default:
		s.reason = reason
		close(s.stopChan)
		return true
	}
}

// inMemoryStreamClient mimics the behavior of a gRPC client stream using channels.
// REQ represents the type sent from the client to the server, RES is the type
// sent from the server to the client.
type inMemoryStreamClient[REQ cloner[REQ], RES any] inMemoryStream[REQ, RES]

func (s *inMemoryStreamClient[REQ, RES]) Send(req REQ) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return io.EOF
	// We clone the data before sending it into the stream to avoid
	// sharing the same data between the server and the client.
	//
	// Load-bearing for arch-v2 destination fan-out (#2910): sibling
	// destination branches share one copy of each record and rely on no
	// destination writing to it. This clone is what keeps a builtin
	// destination's edits off the shared records. See
	// docs/design-documents/20261008-archv2-fanout-shared-records.md.
	case s.reqChan <- req.Clone():
		return nil
	}
}

func (s *inMemoryStreamClient[REQ, RES]) Recv() (RES, error) {
	select {
	case <-s.ctx.Done():
		return s.emptyRes(), s.ctx.Err()
	case <-s.stopChan:
		return s.emptyRes(), s.reason // client receives the reason for closing
	case resp := <-s.respChan:
		return resp, nil
	}
}

func (s *inMemoryStreamClient[REQ, RES]) emptyRes() RES {
	var r RES
	return r
}

// inMemoryStreamServer mimics the behavior of a gRPC server stream using channels.
// REQ represents the type sent from the client to the server, RES is the type
// sent from the server to the client.
type inMemoryStreamServer[REQ any, RES cloner[RES]] inMemoryStream[REQ, RES]

func (s *inMemoryStreamServer[REQ, RES]) Send(resp RES) error {
	// Invariant: when cloneServerResponse is false (the source stream), the
	// server transfers ownership of resp to the client and must not touch it
	// again. That holds because the SDK source only reads the last record's
	// Position after Send, and the engine never mutates Position in place. If
	// a source plugin were to reuse record data after Send, or the engine
	// started mutating Position, this would need to be a clone again — the
	// load-bearing destination-side clone in inMemoryStreamClient.Send is
	// unaffected (#2910).
	if s.cloneServerResponse {
		resp = resp.Clone()
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return io.EOF
	case s.respChan <- resp:
		return nil
	}
}

func (s *inMemoryStreamServer[REQ, RES]) Recv() (REQ, error) {
	select {
	case <-s.ctx.Done():
		return s.emptyReq(), s.ctx.Err()
	case <-s.stopChan:
		return s.emptyReq(), io.EOF
	case req := <-s.reqChan:
		return req, nil
	}
}

func (s *inMemoryStreamServer[REQ, RES]) emptyReq() REQ {
	var r REQ
	return r
}

type cloner[T any] interface {
	Clone() T
}
