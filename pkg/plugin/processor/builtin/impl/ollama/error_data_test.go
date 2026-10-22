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
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
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
		return nil, &url.Error{Op: "Post", URL: "http://SECRET-user:SECRET-pass@ollama.internal:11434/api/generate", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	})

	ctx := context.Background()
	p := NewOllamaProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{ollamaProcessorConfigUrl: "http://ollama.internal:11434"}))

	out := p.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}}}})
	er, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(strings.Contains(er.Error.Error(), "connection refused"))
	is.True(!strings.Contains(er.Error.Error(), "SECRET"))
	is.True(!strings.Contains(er.Error.Error(), "ollama.internal")) // the host can come from record data
}

// The record's JSON is data: a syntax error keeps its offset, not the text.
func TestOllamaProcessor_InvalidRecordJSONDoesNotContainTheValue(t *testing.T) {
	p := NewOllamaProcessor(log.Nop()).(*OllamaProcessor)
	for name, in := range map[string]any{
		"raw":    opencdc.RawData(`{"a": customer-SECRET-4711}`),
		"string": `{"a": customer-SECRET-4711}`,
		"bytes":  []byte(`{"a": customer-SECRET-4711}`),
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			_, err := p.structuredData(in)
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), "invalid JSON syntax at offset"))
			is.True(!strings.Contains(err.Error(), "SECRET"))
		})
	}
}
