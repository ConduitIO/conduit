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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// runSandbox takes a function and runs it in a sandboxed mode that catches
// panics and converts them into an error instead.
// It is specifically designed to run functions that take a context and a
// request and return a response and an error (i.e. plugin calls).
func runSandbox[REQ any, RES any](
	f func(context.Context, REQ) (RES, error),
	ctx context.Context,
	req REQ,
) (res RES, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr, ok := r.(error)
			if !ok {
				panicErr = cerrors.Errorf("panic: %v", r)
			}
			err = panicErr
		}
	}()
	return f(ctx, req)
}

func runSandboxNoResp[REQ any](
	f func(context.Context, REQ) error,
	ctx context.Context,
	req REQ,
) error {
	_, err := runSandbox(func(ctx context.Context, req REQ) (any, error) {
		err := f(ctx, req)
		return nil, err
	}, ctx, req)
	return err
}
