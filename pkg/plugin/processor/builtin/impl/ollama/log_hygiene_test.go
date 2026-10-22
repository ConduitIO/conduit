// Copyright © 2026 Meroxa, Inc.
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

package ollama

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// TestOllamaProcessor_TraceLogDoesNotContainRequestBody: the request body
// holds the record's data, so the trace line carries its size only.
func TestOllamaProcessor_TraceLogDoesNotContainRequestBody(t *testing.T) {
	is := is.New(t)
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	client := setupHTTPMockClient(t)
	client.EXPECT().Do(gomock.Any()).DoAndReturn(getOllamaResp(t)) //nolint:bodyclose // the mock response body is a NopCloser

	// sdk.Logger derives from the SDK's package-level logger.
	var buf bytes.Buffer
	prevLogger := pprocutils.Logger
	pprocutils.Logger = zerolog.New(&buf).Level(zerolog.TraceLevel)
	t.Cleanup(func() { pprocutils.Logger = prevLogger })
	ctx := context.Background()

	p := NewOllamaProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{ollamaProcessorConfigUrl: "http://localhost:11434"}))

	const secret = "ssn-SECRET-4711"
	got := p.Process(ctx, []opencdc.Record{{
		Payload: opencdc.Change{After: opencdc.StructuredData{"note": secret}},
	}})
	is.Equal(len(got), 1)

	out := buf.String()
	is.True(strings.Contains(out, "sending Ollama request"))
	is.True(strings.Contains(out, `"request_size"`))
	is.True(!strings.Contains(out, secret))
}
