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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestProcessors_TraceLogDoesNotContainRecordKey: the trace line for a record
// carries the key's size and a salted hash, never the key.
func TestProcessors_TraceLogDoesNotContainRecordKey(t *testing.T) {
	const secret = "customer-SECRET-4711"
	rec := opencdc.Record{
		Key:     opencdc.RawData(secret),
		Payload: opencdc.Change{After: opencdc.RawData("some text")},
	}

	tests := []struct {
		name string
		run  func(context.Context, log.CtxLogger) error
	}{{
		name: "command",
		run: func(ctx context.Context, l log.CtxLogger) error {
			p := NewCommandProcessor(l)
			if err := p.Configure(ctx, config.Config{commandProcessorConfigApiKey: "apikey", commandProcessorConfigPrompt: "p"}); err != nil {
				return err
			}
			p.client = &mockCommandClient{}
			p.Process(ctx, []opencdc.Record{rec})
			return nil
		},
	}, {
		name: "rerank",
		run: func(ctx context.Context, l log.CtxLogger) error {
			p := NewRerankProcessor(l)
			if err := p.Configure(ctx, config.Config{rerankConfigApiKey: "apikey", rerankConfigQuery: "q"}); err != nil {
				return err
			}
			p.client = &mockRerankClient{}
			p.Process(ctx, []opencdc.Record{rec})
			return nil
		},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			prev := zerolog.GlobalLevel()
			zerolog.SetGlobalLevel(zerolog.TraceLevel)
			t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

			var buf bytes.Buffer
			is.NoErr(tc.run(context.Background(), log.New(zerolog.New(&buf).Level(zerolog.TraceLevel))))

			out := buf.String()
			is.True(strings.Contains(out, "processing record"))
			is.True(strings.Contains(out, `"record_key_hash"`))
			is.True(strings.Contains(out, `"record_key_size":20`))
			is.True(!strings.Contains(out, secret))
		})
	}
}
