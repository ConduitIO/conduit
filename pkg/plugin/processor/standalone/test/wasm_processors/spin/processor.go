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

//go:build wasm

// Package main is a test processor whose Specification() never returns and
// never calls the host: it spins on the CPU. It models a guest stuck in an
// infinite loop, which the host can only stop by closing the module from inside
// the guest's own call (wazero's close-on-context-done), not by waiting for it
// or by closing the module from another goroutine (#2981).
package main

import (
	"context"
	"sync/atomic"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

var counter atomic.Uint64

func main() {
	sdk.Run(&testProcessor{})
}

type testProcessor struct {
	sdk.UnimplementedProcessor
}

func (p *testProcessor) Specification() (sdk.Specification, error) {
	for {
		counter.Add(1)
	}
}

func (p *testProcessor) Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord {
	panic("shouldn't be called")
}
