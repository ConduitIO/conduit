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

package custom

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// BenchmarkJSProcessor_Process measures the full Process path (goja call,
// export, output guard) for a trivial script over a 20-field structured
// record. It does not reference the output guard, so it compiles on a
// branch without it and the numbers are directly comparable.
func BenchmarkJSProcessor_Process(b *testing.B) {
	p := NewJavascriptProcessor(log.Nop())
	ctx := context.Background()
	if err := p.Configure(ctx, config.Config{"script": `function process(r) { r.Metadata["x"] = "y"; return r; }`}); err != nil {
		b.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		b.Fatal(err)
	}

	after := opencdc.StructuredData{}
	for i := 0; i < 20; i++ {
		after[fmt.Sprintf("field%d", i)] = i
	}
	rec := opencdc.Record{
		Key:      opencdc.RawData("k"),
		Metadata: opencdc.Metadata{},
		Payload:  opencdc.Change{After: after},
	}
	recs := []opencdc.Record{rec}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Process(ctx, recs)
	}
}
