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
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/schemaregistry"
)

// processorWithID is the outermost wrapper of every built-in processor. Each
// call into the processor gets the processor ID (for logs) and the schema
// service of the runtime that created the processor (see schema_scope.go).
type processorWithID struct {
	sdk.Processor
	id string
	// schemaService is the owning runtime's schema service. It is read by
	// scopedSchemaService when the processor, or the SDK middleware wrapped
	// inside it, calls schema.Get or schema.Create. Nil only in tests that
	// do not resolve schemas.
	schemaService pprocutils.SchemaService
}

func newProcessorWithID(processor sdk.Processor, id string, schemaService pprocutils.SchemaService) *processorWithID {
	return &processorWithID{
		Processor:     processor,
		id:            id,
		schemaService: schemaService,
	}
}

// callContext returns the context for a call into the wrapped processor.
func (p *processorWithID) callContext(ctx context.Context) context.Context {
	ctx = ctxutil.ContextWithProcessorID(ctx, p.id)
	if p.schemaService != nil {
		// Invariant 6: the processor resolves schemas in its own runtime's
		// registry, never in the registry of another runtime in the process.
		ctx = contextWithSchemaService(ctx, p.schemaService)
	}
	return ctx
}

func (p *processorWithID) Configure(ctx context.Context, cfg config.Config) error {
	ctx = p.callContext(ctx)
	return p.Processor.Configure(ctx, cfg)
}

func (p *processorWithID) Open(ctx context.Context) error {
	ctx = p.callContext(ctx)
	return p.Processor.Open(ctx)
}

// Process runs the wrapped processor and tags Avro element-limit failures
// with schemaregistry.CodeAvroLimitExceeded. Built-in processors get the
// processor-sdk schema decode middleware (see Registry.NewProcessor), whose
// errors carry no code of their own.
func (p *processorWithID) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	ctx = p.callContext(ctx)
	out := p.Processor.Process(ctx, records)
	for i, rec := range out {
		if er, ok := rec.(sdk.ErrorRecord); ok {
			er.Error = schemaregistry.ClassifyDecodeError(er.Error)
			out[i] = er
		}
	}
	return out
}

func (p *processorWithID) Teardown(ctx context.Context) error {
	ctx = p.callContext(ctx)
	return p.Processor.Teardown(ctx)
}
