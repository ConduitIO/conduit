// Copyright © 2024 Meroxa, Inc.
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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
)

type processorWithID struct {
	sdk.Processor
	id string
}

func newProcessorWithID(processor sdk.Processor, id string) *processorWithID {
	return &processorWithID{
		Processor: processor,
		id:        id,
	}
}

func (p *processorWithID) Configure(ctx context.Context, cfg config.Config) error {
	ctx = ctxutil.ContextWithProcessorID(ctx, p.id)
	return p.Processor.Configure(ctx, cfg)
}

func (p *processorWithID) Open(ctx context.Context) error {
	ctx = ctxutil.ContextWithProcessorID(ctx, p.id)
	return p.Processor.Open(ctx)
}

func (p *processorWithID) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	ctx = ctxutil.ContextWithProcessorID(ctx, p.id)
	return p.Processor.Process(ctx, records)
}

func (p *processorWithID) Teardown(ctx context.Context) error {
	ctx = ctxutil.ContextWithProcessorID(ctx, p.id)
	return p.Processor.Teardown(ctx)
}
