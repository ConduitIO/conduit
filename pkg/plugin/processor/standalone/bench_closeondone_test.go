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

package standalone

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// BenchmarkWASMProcessor_Process measures per-batch Process latency with
// wazero's close-on-context-done off and on, for both engines, to quantify what
// wasmengine.New's WithCloseOnContextDone(true) costs on the record path (#2981).
//
// Run (compiler engine is the production default on amd64/arm64):
//
//	go test ./pkg/plugin/processor/standalone -run '^$' -bench WASMProcessor_Process -count=10
//
// and compare the closeOnDone=false/true pairs with benchstat. The chaos test
// processor does trivial work per record, so this is a worst case for relative
// overhead: real processors spend more time per record in the guest itself.
func BenchmarkWASMProcessor_Process(b *testing.B) {
	engines := map[string]func() wazero.RuntimeConfig{
		"compiler":    wazero.NewRuntimeConfigCompiler,
		"interpreter": wazero.NewRuntimeConfigInterpreter,
	}
	for _, engine := range []string{"compiler", "interpreter"} {
		for _, closeOnDone := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/closeOnDone=%v", engine, closeOnDone), func(b *testing.B) {
				ctx := context.Background()
				rt := wazero.NewRuntimeWithConfig(ctx, engines[engine]().WithCloseOnContextDone(closeOnDone))
				defer rt.Close(ctx)
				if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
					b.Fatal(err)
				}
				host, err := wazergo.Compile(ctx, rt, hostModule)
				if err != nil {
					b.Fatal(err)
				}
				mod, err := rt.CompileModule(ctx, ChaosProcessorBinary)
				if err != nil {
					b.Fatal(err)
				}
				p, err := newWASMProcessor(ctx, rt, mod, host, schema.NewInMemoryService(), egress.DenyAll(), "bench", log.Nop())
				if err != nil {
					b.Fatal(err)
				}
				defer func() { _ = p.Teardown(ctx) }()
				if err := p.Configure(ctx, map[string]string{"process.prefix": "x"}); err != nil {
					b.Fatal(err)
				}

				records := make([]opencdc.Record, 10)
				for i := range records {
					records[i] = opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData("payload")}}
				}

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for _, r := range p.Process(ctx, records) {
						if _, ok := r.(interface{ error }); ok {
							b.Fatal("process error")
						}
					}
				}
			})
		}
	}
}
