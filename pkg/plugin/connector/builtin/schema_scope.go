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

	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit-connector-sdk/schema"
	"github.com/conduitio/conduit/pkg/plugin"
)

// Built-in connectors reach the schema registry through the connector SDK's
// package-level schema.Service (the SDK's schema extraction and decoding
// middleware calls schema.Get and schema.Create). That variable is
// process-wide, but a process can hold several runtimes (embed API), each with
// its own registry and connector token issuer. Instead of pointing the
// variable at one runtime's service, Conduit installs scopedSchemaService
// once, and every call into a built-in connector carries its runtime's
// service in the context (see the plugin adapters' pluginContext). The router
// forwards to the service in the context, which still checks the connector
// token.
//
// See docs/design-documents/20261014-runtime-scoped-builtin-schema-services.md
// and https://github.com/ConduitIO/conduit/issues/3088.

type schemaServiceCtxKey struct{}

// contextWithSchemaService returns ctx carrying svc as the schema service of
// the runtime the called built-in connector belongs to. A nil svc leaves ctx
// unchanged.
func contextWithSchemaService(ctx context.Context, svc pconnutils.SchemaService) context.Context {
	if svc == nil {
		return ctx
	}
	return context.WithValue(ctx, schemaServiceCtxKey{}, svc)
}

func schemaServiceFromContext(ctx context.Context) pconnutils.SchemaService {
	svc, _ := ctx.Value(schemaServiceCtxKey{}).(pconnutils.SchemaService)
	return svc
}

// scopedSchemaService is the process-wide connector SDK schema service. It
// holds no state: the runtime is chosen per call from the context.
type scopedSchemaService struct{}

var _ pconnutils.SchemaService = scopedSchemaService{}

func (scopedSchemaService) CreateSchema(ctx context.Context, req pconnutils.CreateSchemaRequest) (pconnutils.CreateSchemaResponse, error) {
	// Invariant 6: a schema is registered only in the registry of the runtime
	// that owns the calling connector. With no scope in the context the call
	// is refused; it is never sent to another runtime.
	svc := schemaServiceFromContext(ctx)
	if svc == nil {
		return pconnutils.CreateSchemaResponse{}, plugin.NewBuiltinSchemaUnscopedError("create", req.Subject)
	}
	return svc.CreateSchema(ctx, req)
}

func (scopedSchemaService) GetSchema(ctx context.Context, req pconnutils.GetSchemaRequest) (pconnutils.GetSchemaResponse, error) {
	// Invariant 6: see CreateSchema.
	svc := schemaServiceFromContext(ctx)
	if svc == nil {
		return pconnutils.GetSchemaResponse{}, plugin.NewBuiltinSchemaUnscopedError("get", req.Subject)
	}
	return svc.GetSchema(ctx, req)
}

var installSchemaServiceOnce sync.Once

// installScopedSchemaService points the connector SDK's schema service at the
// router. It runs once per process, so creating another runtime never writes
// the variable while running connectors read it.
func installScopedSchemaService() {
	installSchemaServiceOnce.Do(func() {
		schema.Service = scopedSchemaService{}
	})
}
