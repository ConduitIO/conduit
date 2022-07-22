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

	"github.com/conduitio/conduit/pkg/plugin"
)

// stream mimics the behavior of a gRPC stream using channels.
// REQ represents the type sent from the client to the server, RES is the type
// sent from the server to the client.
type stream[REQ any, RES any] struct {
	ctx      context.Context
	reqChan  chan REQ
	respChan chan RES
	stopChan chan struct{}

	reason error
	m      sync.Mutex
}

func (s *stream[REQ, RES]) Send(resp RES) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return io.EOF
	case s.respChan <- resp:
		return nil
	}
}

func (s *stream[REQ, RES]) Recv() (REQ, error) {
	select {
	case <-s.ctx.Done():
		return s.emptyReq(), s.ctx.Err()
	case <-s.stopChan:
		return s.emptyReq(), io.EOF
	case req := <-s.reqChan:
		return req, nil
	}
}

func (s *stream[REQ, RES]) recvInternal() (RES, error) {
	select {
	case <-s.ctx.Done():
		return s.emptyRes(), s.ctx.Err()
	case <-s.stopChan:
		return s.emptyRes(), s.reason
	case resp := <-s.respChan:
		return resp, nil
	}
}

func (s *stream[REQ, RES]) sendInternal(req REQ) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.stopChan:
		return plugin.ErrStreamNotOpen
	case s.reqChan <- req:
		return nil
	}
}

func (s *stream[REQ, RES]) stop(reason error) bool {
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

func (s *stream[REQ, RES]) emptyReq() REQ {
	var r REQ
	return r
}

func (s *stream[REQ, RES]) emptyRes() RES {
	var r RES
	return r
}
