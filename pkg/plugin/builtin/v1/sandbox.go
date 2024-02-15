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
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

var sandboxChanPool = sync.Pool{
	New: func() any {
		return make(chan any)
	},
}

// runSandbox takes a function and runs it in a sandboxed mode that catches
// panics and converts them into an error instead.
// It is specifically designed to run functions that take a context and a
// request and return a response and an error (i.e. plugin calls).
func runSandbox[REQ any, RES any](
	f func(context.Context, REQ) (RES, error),
	ctx context.Context, // context is the second parameter on purpose
	req REQ,
	logger log.CtxLogger,
) (RES, error) {
	c := sandboxChanPool.Get().(chan any)

	returnResponse := func(ctx context.Context, res RES, err error) {
		defer sandboxChanPool.Put(c)
		select {
		case <-ctx.Done():
			// The context was cancelled, nobody will fetch the result.
			logger.Error(ctx).
				Any("response", res).
				Err(err).
				Msg("context cancelled when trying to return response from builtin connector plugin (this message comes from a detached plugin)")
		case c <- res:
			// The result was sent, now send the error if any and return the
			// channel to the pool.
			c <- err
		}
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				err, ok := r.(error)
				if !ok {
					err = cerrors.Errorf("panic: %v", r)
				}
				// return the panic error
				var emptyRes RES
				returnResponse(ctx, emptyRes, err)
			}
		}()

		res, err := f(ctx, req)
		returnResponse(ctx, res, err)
	}()

	select {
	case <-ctx.Done():
		// Context was cancelled, detach from calling goroutine and return.
		logger.Error(ctx).Msg("context cancelled while waiting for builtin connector plugin to respond, detaching from plugin")
		var emptyRes RES
		return emptyRes, ctx.Err()
	case v := <-c:
		var res RES
		var err error

		// We got a response, which means the goroutine will send another value
		// (the error) and then return the channel to the pool.
		if v != nil {
			res = v.(RES)
		}
		v = <-c
		if v != nil {
			err = v.(error)
		}
		return res, err
	}
}

func runSandboxNoResp[REQ any](
	f func(context.Context, REQ) error,
	ctx context.Context, // context is the second parameter on purpose
	req REQ,
	logger log.CtxLogger,
) error {
	_, err := runSandbox(func(ctx context.Context, req REQ) (any, error) {
		err := f(ctx, req)
		return nil, err
	}, ctx, req, logger)
	return err
}
