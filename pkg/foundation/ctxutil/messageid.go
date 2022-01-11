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

// messageIDCtxKey is used as the key when saving the message ID in a context.
type messageIDCtxKey struct{}

// ContextWithMessageID wraps ctx and returns a context that contains message ID.
func ContextWithMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIDCtxKey{}, messageID)
}

// MessageIDFromContext fetches the record message ID from the context. If the
// context does not contain a message ID it returns nil.
func MessageIDFromContext(ctx context.Context) string {
	messageID := ctx.Value(messageIDCtxKey{})
	if messageID != nil {
		return messageID.(string)
	}
	return ""
}

// MessageIDLogCtxHook fetches the record message ID from the context and if it
// exists it adds the messageID to the log output.
type MessageIDLogCtxHook struct{}

// Run executes the log hook.
func (h MessageIDLogCtxHook) Run(ctx context.Context, e *zerolog.Event, lvl zerolog.Level) {
	p := MessageIDFromContext(ctx)
	if p != "" {
		e.Str(log.MessageIDField, p)
	}
}
