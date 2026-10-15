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
	"sync"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/plugin"
)

// Built-in processors reach the schema registry through the processor SDK's
// package-level schema.SchemaService (the SDK's schema decode/encode
// middleware calls schema.Get and schema.Create). That variable is
// process-wide, but a process can hold several runtimes (embed API), each
// with its own registry. Instead of pointing the variable at one runtime's
// service, Conduit installs scopedSchemaService once, and every call into a
// built-in processor carries its runtime's service in the context (see
// processorWithID). The router forwards to the service in the context.
//
// See docs/design-documents/20261014-runtime-scoped-builtin-schema-services.md
// and https://github.com/ConduitIO/conduit/issues/3088.

type schemaServiceCtxKey struct{}

// contextWithSchemaService returns ctx carrying svc as the schema service of
// the runtime the called built-in processor belongs to.
func contextWithSchemaService(ctx context.Context, svc pprocutils.SchemaService) context.Context {
	return context.WithValue(ctx, schemaServiceCtxKey{}, svc)
}

func schemaServiceFromContext(ctx context.Context) pprocutils.SchemaService {
	svc, _ := ctx.Value(schemaServiceCtxKey{}).(pprocutils.SchemaService)
	return svc
}

// scopedSchemaService is the process-wide processor SDK schema service. It
// holds no state: the runtime is chosen per call from the context.
type scopedSchemaService struct{}

var _ pprocutils.SchemaService = scopedSchemaService{}

func (scopedSchemaService) GetSchema(ctx context.Context, req pprocutils.GetSchemaRequest) (pprocutils.GetSchemaResponse, error) {
	// Invariant 6: a schema lookup is answered only by the registry of the
	// runtime that owns the calling processor. With no scope in the context
	// the call is refused; it is never answered from another runtime.
	svc := schemaServiceFromContext(ctx)
	if svc == nil {
		return pprocutils.GetSchemaResponse{}, plugin.NewBuiltinSchemaUnscopedError("get", req.Subject)
	}
	return svc.GetSchema(ctx, req)
}

func (scopedSchemaService) CreateSchema(ctx context.Context, req pprocutils.CreateSchemaRequest) (pprocutils.CreateSchemaResponse, error) {
	// Invariant 6: see GetSchema.
	svc := schemaServiceFromContext(ctx)
	if svc == nil {
		return pprocutils.CreateSchemaResponse{}, plugin.NewBuiltinSchemaUnscopedError("create", req.Subject)
	}
	return svc.CreateSchema(ctx, req)
}

var installSchemaServiceOnce sync.Once

// installScopedSchemaService points the processor SDK's schema service at the
// router. It runs once per process, so creating another runtime never writes
// the variable while running processors read it.
func installScopedSchemaService() {
	installSchemaServiceOnce.Do(func() {
		schema.SchemaService = scopedSchemaService{}
	})
}
