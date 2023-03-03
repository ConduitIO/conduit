// Copyright © 2023 Meroxa, Inc.
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

// filepathCtxKey is used as the key when saving the filepath in a context.
type filepathCtxKey struct{}

// ContextWithFilepath wraps ctx and returns a context that contains filepath.
func ContextWithFilepath(ctx context.Context, filepath string) context.Context {
	return context.WithValue(ctx, filepathCtxKey{}, filepath)
}

// FilepathFromContext fetches the record filepath from the context. If the
// context does not contain a filepath it returns nil.
func FilepathFromContext(ctx context.Context) string {
	filepath := ctx.Value(filepathCtxKey{})
	if filepath != nil {
		return filepath.(string)
	}
	return ""
}

// FilepathLogCtxHook fetches the record filepath from the context and if it
// exists it adds the filepath to the log output.
type FilepathLogCtxHook struct{}

// Run executes the log hook.
func (h FilepathLogCtxHook) Run(ctx context.Context, e *zerolog.Event, lvl zerolog.Level) {
	p := FilepathFromContext(ctx)
	if p != "" {
		e.Str(log.FilepathField, p)
	}
}
