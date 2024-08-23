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

package standalone

import (
	"bytes"
	"context"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit-processor-sdk/pprocutils/v1/fromproto"
	"github.com/conduitio/conduit-processor-sdk/pprocutils/v1/toproto"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	procutilsv1 "github.com/conduitio/conduit-processor-sdk/proto/procutils/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/stealthrocket/wazergo"
	"github.com/stealthrocket/wazergo/types"
	"google.golang.org/protobuf/proto"
)

// hostModule declares the host module that is exported to the WASM module. The
// host module is used to communicate between the WASM module (processor) and Conduit.
var hostModule wazergo.HostModule[*hostModuleInstance] = hostModuleFunctions{
	"command_request":  wazergo.F1((*hostModuleInstance).commandRequest),
	"command_response": wazergo.F1((*hostModuleInstance).commandResponse),
	"create_schema":    wazergo.F1((*hostModuleInstance).createSchema),
	"get_schema":       wazergo.F1((*hostModuleInstance).getSchema),
}

// hostModuleFunctions type implements HostModule, providing the module name,
// map of exported functions, and the ability to create instances of the module
// type.
type hostModuleFunctions wazergo.Functions[*hostModuleInstance]

// Name returns the name of the module.
func (f hostModuleFunctions) Name() string {
	return "conduit"
}

// Functions is a helper that returns the exported functions of the module.
func (f hostModuleFunctions) Functions() wazergo.Functions[*hostModuleInstance] {
	return (wazergo.Functions[*hostModuleInstance])(f)
}

// Instantiate creates a new instance of the module. This is called by the
// runtime when a new instance of the module is created.
func (f hostModuleFunctions) Instantiate(_ context.Context, opts ...hostModuleOption) (*hostModuleInstance, error) {
	mod := &hostModuleInstance{
		parkedResponses:  make(map[string]proto.Message),
		lastRequestBytes: make(map[string][]byte),
	}
	wazergo.Configure(mod, opts...)
	if mod.commandRequests == nil {
		return nil, cerrors.New("missing command requests channel")
	}
	if mod.commandResponses == nil {
		return nil, cerrors.New("missing command responses channel")
	}
	return mod, nil
}

type hostModuleOption = wazergo.Option[*hostModuleInstance]

func hostModuleOptions(
	logger log.CtxLogger,
	requests <-chan *processorv1.CommandRequest,
	responses chan<- tuple[*processorv1.CommandResponse, error],
	schemaService pprocutils.SchemaService,
) hostModuleOption {
	return wazergo.OptionFunc(func(m *hostModuleInstance) {
		m.logger = logger
		m.commandRequests = requests
		m.commandResponses = responses
		m.schemaService = schemaService
	})
}

// hostModuleInstance is used to maintain the state of our module instance.
type hostModuleInstance struct {
	logger           log.CtxLogger
	commandRequests  <-chan *processorv1.CommandRequest
	commandResponses chan<- tuple[*processorv1.CommandResponse, error]
	schemaService    pprocutils.SchemaService

	parkedCommandRequest *processorv1.CommandRequest
	parkedResponses      map[string]proto.Message
	lastRequestBytes     map[string][]byte
}

func (*hostModuleInstance) Close(context.Context) error { return nil }

// commandRequest is the exported function that is called by the WASM module to
// get the next command request. It returns the size of the command request
// message. If the buffer is too small, it returns the size of the command
// request message and parks the command request. The next call to this function
// will return the same command request.
func (m *hostModuleInstance) commandRequest(ctx context.Context, buf types.Bytes) types.Uint64 {
	m.logger.Trace(ctx).Msg("executing command_request")

	if m.parkedCommandRequest == nil {
		// No parked command, so we need to wait for the next one. If the command
		// channel is closed, then we return an error.
		var ok bool
		m.parkedCommandRequest, ok = <-m.commandRequests
		if !ok {
			return pprocutils.ErrorCodeNoMoreCommands
		}
	}

	// If the buffer is too small, we park the command and return the size of the
	// command. The next call to nextCommand will return the same command.
	if size := proto.Size(m.parkedCommandRequest); len(buf) < size {
		m.logger.Warn(ctx).
			Int("command_bytes", size).
			Int("allocated_bytes", len(buf)).
			Msgf("insufficient memory, command will be parked until next call to command_request")
		return types.Uint64(size)
	}

	// If the buffer is large enough, we marshal the command into the buffer and
	// return the size of the command. The next call to nextCommand will return
	// the next command.
	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedCommandRequest)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf command request")
		return pprocutils.ErrorCodeUnknownCommandRequest
	}
	m.parkedCommandRequest = nil

	m.logger.Trace(ctx).Msg("returning next command")
	return types.Uint64(len(out))
}

// commandResponse is the exported function that is called by the WASM module to
// send a command response. It returns 0 on success, or an error code on error.
func (m *hostModuleInstance) commandResponse(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing command_response")

	var resp processorv1.CommandResponse
	err := proto.Unmarshal(buf, &resp)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed unmarshalling protobuf command response")
		m.commandResponses <- tuple[*processorv1.CommandResponse, error]{nil, err}
		return pprocutils.ErrorCodeUnknownCommandResponse
	}

	m.commandResponses <- tuple[*processorv1.CommandResponse, error]{&resp, nil}
	return 0
}

// handleWasmRequest is a helper function that handles WASM requests. It
// unmarshalls the request, calls the service function, and marshals the response.
// If the buffer is too small, it parks the response and returns the size of the
// response. The next call to this method will return the same response.
func (m *hostModuleInstance) handleWasmRequest(
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
		return types.Uint32(size)
	}

	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], parkedResponse)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf create schema response")
		return pprocutils.ErrorCodeInternal
	}

	m.lastRequestBytes[serviceMethod] = nil
	m.parkedResponses[serviceMethod] = nil

	return types.Uint32(len(out))
}

// createSchema is the exported function that is called by the WASM module to
// create a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success, or an error code on error.
func (m *hostModuleInstance) createSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
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

	return m.handleWasmRequest(ctx, buf, "create_schema", serviceFunc)
}

// getSchema is the exported function that is called by the WASM module to
// get a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success, or an error code on error.
func (m *hostModuleInstance) getSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
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

	return m.handleWasmRequest(ctx, buf, "get_schema", serviceFunc)
}
