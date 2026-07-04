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

package common

import (
	"bytes"
	"context"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/pprocutils/v1/fromproto"
	"github.com/conduitio/conduit-processor-sdk/pprocutils/v1/toproto"
	procutilsv1 "github.com/conduitio/conduit-processor-sdk/proto/procutils/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/stealthrocket/wazergo"
	"github.com/stealthrocket/wazergo/types"
	"google.golang.org/protobuf/proto"
)

// HostModule declares the host module that is exported to the WASM module. The
// host module is used to communicate between the WASM module (processor) and Conduit.
var HostModule wazergo.HostModule[*HostModuleInstance] = HostModuleFunctions{
	"create_schema": wazergo.F1((*HostModuleInstance).CreateSchema),
	"get_schema":    wazergo.F1((*HostModuleInstance).GetSchema),
}

// HostModuleFunctions type implements HostModule, providing the module name,
// map of exported functions, and the ability to create instances of the module
// type.
type HostModuleFunctions wazergo.Functions[*HostModuleInstance]

// Name returns the name of the module.
func (f HostModuleFunctions) Name() string {
	return "conduit"
}

// Functions is a helper that returns the exported functions of the module.
func (f HostModuleFunctions) Functions() wazergo.Functions[*HostModuleInstance] {
	return (wazergo.Functions[*HostModuleInstance])(f)
}

// Instantiate creates a new instance of the module. This is called by the
// runtime when a new instance of the module is created.
func (f HostModuleFunctions) Instantiate(_ context.Context, opts ...HostModuleOption) (*HostModuleInstance, error) {
	mod := NewHostModuleInstance()
	wazergo.Configure(mod, opts...)
	return mod, nil
}

type HostModuleOption = wazergo.Option[*HostModuleInstance]

func HostModuleOptions(
	logger log.CtxLogger,
	schemaService pprocutils.SchemaService,
) HostModuleOption {
	return wazergo.OptionFunc(func(m *HostModuleInstance) {
		m.logger = logger
		m.schemaService = schemaService
	})
}

// HostModuleInstance is used to maintain the state of our module instance.
type HostModuleInstance struct {
	logger        log.CtxLogger
	schemaService pprocutils.SchemaService
	
	parkedResponses  map[string]proto.Message
	lastRequestBytes map[string][]byte
}

func NewHostModuleInstance() *HostModuleInstance {
	return &HostModuleInstance{
		parkedResponses:  make(map[string]proto.Message),
		lastRequestBytes: make(map[string][]byte),
	}
}

func (*HostModuleInstance) Close(context.Context) error { return nil }

// HandleWasmRequest is a helper function that handles WASM requests. It
// unmarshalls the request, calls the service function, and marshals the response.
// If the buffer is too small, it parks the response and returns the size of the
// response. The next call to this method will return the same response.
func (m *HostModuleInstance) HandleWasmRequest(
	ctx context.Context,
	buf types.Bytes,
	serviceMethod string,
	serviceFunc func(ctx context.Context, buf types.Bytes) (proto.Message, error),
) types.Uint32 {
	lastRequestBytes := m.lastRequestBytes[serviceMethod]
	parkedResponse := m.parkedResponses[serviceMethod]

	// no request/response parked or the buffer contains a new request
	if parkedResponse == nil ||
		(len(buf) >= len(lastRequestBytes) &&
			!bytes.Equal(lastRequestBytes, buf[:len(lastRequestBytes)])) {
		resp, err := serviceFunc(ctx, buf)
		if err != nil {
			m.logger.Err(ctx, err).
				Str("method", serviceMethod).
				Msg("failed executing service method")
			var pErr *pprocutils.Error
			if cerrors.As(err, &pErr) {
				return types.Uint32(pErr.ErrCode)
			}
			return pprocutils.ErrorCodeInternal
		}
		lastRequestBytes = buf
		parkedResponse = resp

		m.lastRequestBytes[serviceMethod] = lastRequestBytes
		m.parkedResponses[serviceMethod] = parkedResponse
	}

	// If the buffer is too small, we park the response and return the size of the
	// response. The next call to this method will return the same response.
	if size := proto.Size(parkedResponse); len(buf) < size {
		m.logger.Warn(ctx).
			Int("response_bytes", size).
			Int("allocated_bytes", len(buf)).
			Msgf("insufficient memory, response will be parked until next call to %s", serviceMethod)
		return types.Uint32(size) //nolint:gosec // no risk of overflow
	}

	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], parkedResponse)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf create schema response")
		return pprocutils.ErrorCodeInternal
	}

	m.lastRequestBytes[serviceMethod] = nil
	m.parkedResponses[serviceMethod] = nil

	return types.Uint32(len(out)) //nolint:gosec // no risk of overflow
}

// CreateSchema is the exported function that is called by the WASM module to
// create a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success, or an error code on error.
func (m *HostModuleInstance) CreateSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing create_schema")

	serviceFunc := func(ctx context.Context, buf types.Bytes) (proto.Message, error) {
		var req procutilsv1.CreateSchemaRequest
		err := proto.Unmarshal(buf, &req)
		if err != nil {
			m.logger.Err(ctx, err).Msg("failed unmarshalling protobuf create schema request")
			return nil, err
		}
		schemaResp, err := m.schemaService.CreateSchema(ctx, fromproto.CreateSchemaRequest(&req))
		if err != nil {
			return nil, err
		}
		return toproto.CreateSchemaResponse(schemaResp), nil
	}

	return m.HandleWasmRequest(ctx, buf, "create_schema", serviceFunc)
}

// GetSchema is the exported function that is called by the WASM module to
// get a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success or an error code on error.
func (m *HostModuleInstance) GetSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing get_schema")

	serviceFunc := func(ctx context.Context, buf types.Bytes) (proto.Message, error) {
		var req procutilsv1.GetSchemaRequest
		err := proto.Unmarshal(buf, &req)
		if err != nil {
			m.logger.Err(ctx, err).Msg("failed unmarshalling protobuf get schema request")
			return nil, err
		}
		schemaResp, err := m.schemaService.GetSchema(ctx, fromproto.GetSchemaRequest(&req))
		if err != nil {
			return nil, err
		}
		return toproto.GetSchemaResponse(schemaResp), nil
	}

	return m.HandleWasmRequest(ctx, buf, "get_schema", serviceFunc)
}
