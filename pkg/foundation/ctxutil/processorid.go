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

package ctxutil

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

// processorIDCtxKey is used as the key when saving the processor ID in a context.
type processorIDCtxKey struct{}

// ContextWithProcessorID wraps ctx and returns a context that contains processor ID.
func ContextWithProcessorID(ctx context.Context, processorID string) context.Context {
	return context.WithValue(ctx, processorIDCtxKey{}, processorID)
}

// ProcessorIDFromContext fetches the record processor ID from the context. If the
// context does not contain a processor ID it returns nil.
func ProcessorIDFromContext(ctx context.Context) string {
	processorID := ctx.Value(processorIDCtxKey{})
	if processorID != nil {
		return processorID.(string)
	}
	return ""
}

// ProcessorIDLogCtxHook fetches the record processor ID from the context and if it
// exists it adds the processorID to the log output.
type ProcessorIDLogCtxHook struct{}

// Run executes the log hook.
func (h ProcessorIDLogCtxHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	p := ProcessorIDFromContext(e.GetCtx())
	if p != "" {
		e.Str(log.ProcessorIDField, p)
	}
}
