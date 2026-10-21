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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	sdk "github.com/conduitio/conduit-processor-sdk"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/cohere"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/ollama"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/openai"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/webhook"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
)

// Every built-in processor that dials out must be able to receive the
// operator's dial guard. If one stops embedding egress.GuardHolder these stop
// compiling.
var (
	_ dialGuardProcessor = (*webhook.HTTPProcessor)(nil)
	_ dialGuardProcessor = (*ollama.OllamaProcessor)(nil)
	_ dialGuardProcessor = (*openai.EmbeddingsProcessor)(nil)
	_ dialGuardProcessor = (*openai.TextgenProcessor)(nil)
	_ dialGuardProcessor = (*cohere.CommandProcessor)(nil)
	_ dialGuardProcessor = (*cohere.EmbedProcessor)(nil)
	_ dialGuardProcessor = (*cohere.RerankProcessor)(nil)

	_ fileAccessProcessor = (*custom.JavascriptProcessor)(nil)
)

type recordingProcessor struct {
	sdk.UnimplementedProcessor
	egress.GuardHolder
	files custom.FileAccess
}

func (p *recordingProcessor) SetFileAccess(fa custom.FileAccess) { p.files = fa }

func (*recordingProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{Name: "recording", Version: "v0.0.1"}, nil
}

func TestRegistry_HandsOperatorPoliciesToProcessors(t *testing.T) {
	is := is.New(t)

	var last *recordingProcessor
	ctor := func(log.CtxLogger) sdk.Processor {
		last = &recordingProcessor{}
		return last
	}
	sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)

	guard := egress.NewDialGuard(nil)
	fa := custom.FileAccess{}
	reg := NewRegistry(log.Nop(), map[string]ProcessorPluginConstructor{"recording": ctor}, sr,
		WithDialGuard(guard), WithJavaScriptFileAccess(fa))

	_, err = reg.NewProcessor(context.Background(), "builtin:recording", "id", egress.DenyAll())
	is.NoErr(err)
	is.Equal(last.Guard(), guard)
}

// The default Registry (no options) still gives processors a usable guard: an
// absent option must not mean an unguarded client.
func TestRegistry_NoOptions_ProcessorsStillGuarded(t *testing.T) {
	is := is.New(t)
	var last *recordingProcessor
	ctor := func(log.CtxLogger) sdk.Processor {
		last = &recordingProcessor{}
		return last
	}
	sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)
	reg := NewRegistry(log.Nop(), map[string]ProcessorPluginConstructor{"recording": ctor}, sr)
	_, err = reg.NewProcessor(context.Background(), "builtin:recording", "id", egress.DenyAll())
	is.NoErr(err)

	c := last.HTTPClient()
	is.True(c != nil)
	is.True(c.Timeout > 0)
}
