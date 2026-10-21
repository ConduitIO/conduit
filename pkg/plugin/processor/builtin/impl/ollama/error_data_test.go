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
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// The Ollama URL is configuration that can carry credentials, and the HTTP
// client repeats it in its errors. Errors reach the DLQ, the API and CLI
// output, so the failure reports the cause without the URL.
func TestOllamaProcessor_RequestErrorDoesNotContainTheURL(t *testing.T) {
	is := is.New(t)
	client := setupHTTPMockClient(t)
	client.EXPECT().Do(gomock.Any()).DoAndReturn(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: "http://SECRET-user:SECRET-pass@ollama.internal:11434/api/generate", Err: cerrors.New("connection refused")}
	})

	ctx := context.Background()
	p := NewOllamaProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{ollamaProcessorConfigUrl: "http://ollama.internal:11434"}))

	out := p.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}}}})
	er, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(strings.Contains(er.Error.Error(), "connection refused"))
	is.True(!strings.Contains(er.Error.Error(), "SECRET"))
}
