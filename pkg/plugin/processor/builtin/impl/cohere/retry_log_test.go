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

package cohere

import (
	"bytes"
	"context"
	"strings"
	"testing"

	api "github.com/cohere-ai/cohere-go/v2"
	"github.com/cohere-ai/cohere-go/v2/core"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

type unavailableCommand struct{}

func (unavailableCommand) command(context.Context, string) (string, error) {
	return "", &api.ServiceUnavailableError{APIError: core.NewAPIError(503, cerrors.New("upstream said: customer-SECRET-4711"))}
}

type unavailableRerank struct{}

func (unavailableRerank) rerank(context.Context, []string) ([]RerankResult, error) {
	return nil, &api.ServiceUnavailableError{APIError: core.NewAPIError(503, cerrors.New("upstream said: customer-SECRET-4711"))}
}

type unavailableEmbed struct{}

func (unavailableEmbed) embed(context.Context, []string) ([][]float64, error) {
	return nil, &api.ServiceUnavailableError{APIError: core.NewAPIError(503, cerrors.New("upstream said: customer-SECRET-4711"))}
}

// The SDK's error text can carry what the API answered, which can repeat the
// request. The retry loop logs each failed attempt at debug level: it logs the
// type of the error, never its text.
func TestProcessors_RetryLogDoesNotContainSDKErrorText(t *testing.T) {
	ctx := context.Background()
	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData("some text")}}
	retry := config.Config{"backoffRetry.count": "1", "backoffRetry.min": "1ms", "backoffRetry.max": "2ms"}
	with := func(c config.Config) config.Config {
		out := config.Config{}
		for k, v := range retry {
			out[k] = v
		}
		for k, v := range c {
			out[k] = v
		}
		return out
	}

	for name, run := range map[string]func(log.CtxLogger) error{
		"command": func(l log.CtxLogger) error {
			p := NewCommandProcessor(l)
			if err := p.Configure(ctx, with(config.Config{commandProcessorConfigApiKey: "k", commandProcessorConfigPrompt: "p"})); err != nil {
				return err
			}
			if err := p.Open(ctx); err != nil {
				return err
			}
			p.client = unavailableCommand{}
			p.Process(ctx, []opencdc.Record{rec})
			return nil
		},
		"rerank": func(l log.CtxLogger) error {
			p := NewRerankProcessor(l)
			if err := p.Configure(ctx, with(config.Config{rerankConfigApiKey: "k", rerankConfigQuery: "q"})); err != nil {
				return err
			}
			if err := p.Open(ctx); err != nil {
				return err
			}
			p.client = unavailableRerank{}
			p.Process(ctx, []opencdc.Record{rec})
			return nil
		},
		"embed": func(l log.CtxLogger) error {
			p := NewEmbedProcessor(l)
			if err := p.Configure(ctx, with(config.Config{embedProcConfigApiKey: "k", embedProcConfigInputType: "classification"})); err != nil {
				return err
			}
			if err := p.Open(ctx); err != nil {
				return err
			}
			p.client = unavailableEmbed{}
			p.Process(ctx, []opencdc.Record{rec})
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			var buf bytes.Buffer
			is.NoErr(run(log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))))
			out := buf.String()
			is.True(strings.Contains(out, "retrying Cohere HTTP request"))
			is.True(strings.Contains(out, `"error_type":"*api.ServiceUnavailableError"`))
			is.True(!strings.Contains(out, "SECRET"))
		})
	}
}
