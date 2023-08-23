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

package ctxutil

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

// requestIDCtxKey is used as the key when saving the request ID in a context.
type requestIDCtxKey struct{}

// ContextWithRequestID wraps ctx and returns a context that contains requestID.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDCtxKey{}, requestID)
}

// RequestIDFromContext fetches the request ID from the context. If the context
// does not contain a request ID it returns nil.
func RequestIDFromContext(ctx context.Context) string {
	requestID := ctx.Value(requestIDCtxKey{})
	if requestID != nil {
		return requestID.(string)
	}
	return ""
}

// RequestIDLogCtxHook fetches the request ID from the context and if it exists
// it adds the request ID to the log output.
type RequestIDLogCtxHook struct{}

// Run executes the log hook.
func (h RequestIDLogCtxHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	p := RequestIDFromContext(e.GetCtx())
	if p != "" {
		e.Str(log.RequestIDField, p)
	}
}
