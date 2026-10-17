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

package main

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

func main() {
	sdk.Run(&echoProcessor{})
}

// echoProcessor returns every record unchanged. It is the identity processor
// the standalone runtime tests use to prove a record round-trips the WASM
// boundary exactly: whatever structured value the host sends (including a wide
// int64, uint64, []byte or decimal), the guest returns it and the host must
// read it back losslessly.
type echoProcessor struct {
	sdk.UnimplementedProcessor
}

func (p *echoProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "echo-processor",
		Summary: "Echoes records back unchanged.",
		Version: "v1.0.0",
		Author:  "Meroxa, Inc.",
	}, nil
}

func (p *echoProcessor) Configure(context.Context, config.Config) error { return nil }

func (p *echoProcessor) Open(context.Context) error { return nil }

func (p *echoProcessor) Teardown(context.Context) error { return nil }

func (p *echoProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, r := range records {
		out[i] = sdk.SingleRecord(r)
	}
	return out
}
