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
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestContextWithFilepath_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	filepath := uuid.NewString()

	ctx = ContextWithFilepath(ctx, filepath)
	got := FilepathFromContext(ctx)

	is.Equal(filepath, got)
}

func TestContextWithFilepath_Twice(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	filepath := uuid.NewString()

	ctx = ContextWithFilepath(ctx, "existing filepath")
	ctx = ContextWithFilepath(ctx, filepath)
	got := FilepathFromContext(ctx)

	is.Equal(filepath, got)
}

func TestFilepathFromContext_Empty(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	got := FilepathFromContext(ctx)

	is.Equal("", got)
}

func TestFilepathLogCtxHook_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	filepath := uuid.NewString()

	ctx = ContextWithFilepath(ctx, filepath)

	var logOutput bytes.Buffer
	logger := zerolog.New(&logOutput)
	e := logger.Info().Ctx(ctx)
	FilepathLogCtxHook{}.Run(e, zerolog.InfoLevel, "")
	e.Send()

	is.Equal(fmt.Sprintf(`{"level":"info","%s":"%s"}`, log.FilepathField, filepath)+"\n", logOutput.String())
}

func TestFilepathLogCtxHook_EmptyCtx(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()

	var logOutput bytes.Buffer
	logger := zerolog.New(&logOutput)
	e := logger.Info().Ctx(ctx)
	FilepathLogCtxHook{}.Run(e, zerolog.InfoLevel, "")
	e.Send()

	is.Equal(`{"level":"info"}`+"\n", logOutput.String())
}
