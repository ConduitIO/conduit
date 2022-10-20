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

package cchan

import (
	"context"
	"time"
)

// Chan is a channel with utility methods.
type Chan[T any] <-chan T

// Recv will try to receive a value from the channel, same as <-c. If the
// context is canceled before a value is received, it will return the context
// error.
func (c Chan[T]) Recv(ctx context.Context) (T, bool, error) {
	select {
	case val, ok := <-c:
		return val, ok, nil
	case <-ctx.Done():
		var empty T
		return empty, false, ctx.Err()
	}
}

// RecvTimeout will try to receive a value from the channel, same as <-c. If the
// context is canceled before a values is received or the timeout is reached, it
// will return the context error.
func (c Chan[T]) RecvTimeout(ctx context.Context, timeout time.Duration) (T, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Recv(ctx)
}
