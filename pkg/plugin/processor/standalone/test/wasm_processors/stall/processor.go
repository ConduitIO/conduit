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

// Package main is a test processor whose Specification() blocks for a while
// before answering. It models a guest that is busy or stuck inside a command
// (what a slow interpreter under -race load looks like to the host), so tests
// can drive the host's "gave up waiting for the response" path
// deterministically, without depending on machine speed (#2981).
package main

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

// stallFor is longer than any test deadline that exercises the stall and short
// enough that a leaked guest goroutine ends soon after the test binary does.
const stallFor = 3 * time.Second

func main() {
	sdk.Run(&testProcessor{})
}

type testProcessor struct {
	sdk.UnimplementedProcessor
}

func (p *testProcessor) Specification() (sdk.Specification, error) {
	time.Sleep(stallFor)
	return sdk.Specification{Name: "stall-processor", Version: "v1.0.0"}, nil
}

func (p *testProcessor) Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord {
	panic("shouldn't be called")
}
