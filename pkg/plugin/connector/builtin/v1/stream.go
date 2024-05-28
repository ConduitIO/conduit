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

package builtinv1

import (
	"context"
	"io"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/cplugin"
)

type InMemoryDestinationRunStream struct {
	stream *inMemoryStream[cplugin.DestinationRunRequest, cplugin.DestinationRunResponse]
}

func (s *InMemoryDestinationRunStream) Init(ctx context.Context) {
	s.stream = &inMemoryStream[cplugin.DestinationRunRequest, cplugin.DestinationRunResponse]{
		ctx:      ctx,
		reqChan:  make(chan cplugin.DestinationRunRequest),
		respChan: make(chan cplugin.DestinationRunResponse),
		stopChan: make(chan struct{}),
	}
}

func (s *InMemoryDestinationRunStream) Client() cplugin.DestinationRunStreamClient {
	return (*inMemoryStreamClient[cplugin.DestinationRunRequest, cplugin.DestinationRunResponse])(s.stream)
}

func (s *InMemoryDestinationRunStream) Server() cplugin.DestinationRunStreamServer {
	return (*inMemoryStreamServer[cplugin.DestinationRunRequest, cplugin.DestinationRunResponse])(s.stream)
}

func (s *InMemoryDestinationRunStream) Close(reason error) bool {
	return s.stream.Close(reason)
}

type InMemorySourceRunStream struct {
	stream *inMemoryStream[cplugin.SourceRunRequest, cplugin.SourceRunResponse]
}

func (s *InMemorySourceRunStream) Init(ctx context.Context) {
	s.stream = &inMemoryStream[cplugin.SourceRunRequest, cplugin.SourceRunResponse]{
		ctx:      ctx,
		reqChan:  make(chan cplugin.SourceRunRequest),
		respChan: make(chan cplugin.SourceRunResponse),
		stopChan: make(chan struct{}),
	}
}

func (s *InMemorySourceRunStream) Client() cplugin.SourceRunStreamClient {
	return (*inMemoryStreamClient[cplugin.SourceRunRequest, cplugin.SourceRunResponse])(s.stream)
}

func (s *InMemorySourceRunStream) Server() cplugin.SourceRunStreamServer {
	return (*inMemoryStreamServer[cplugin.SourceRunRequest, cplugin.SourceRunResponse])(s.stream)
}

func (s *InMemorySourceRunStream) Close(reason error) bool {
	return s.stream.Close(reason)
}

type inMemoryStream[REQ any, RES any] struct {
	ctx      context.Context //nolint:containedctx // We need to mimic the behavior of a gRPC stream
	reqChan  chan REQ
	respChan chan RES
	stopChan chan struct{}

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
type inMemoryStreamClient[REQ any, RES any] inMemoryStream[REQ, RES]

func (s *inMemoryStreamClient[REQ, RES]) Send(req REQ) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return io.EOF
	case s.reqChan <- req:
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
type inMemoryStreamServer[REQ any, RES any] inMemoryStream[REQ, RES]

func (s *inMemoryStreamServer[REQ, RES]) Send(resp RES) error {
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
