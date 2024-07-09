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

	schemav1 "github.com/conduitio/conduit-commons/schema"
	conduitv1 "github.com/conduitio/conduit-processor-sdk/proto/conduit/v1"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	pschema "github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit-processor-sdk/wasm"
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
	mod := &hostModuleInstance{}
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
) hostModuleOption {
	return wazergo.OptionFunc(func(m *hostModuleInstance) {
		m.logger = logger
		m.commandRequests = requests
		m.commandResponses = responses
	})
}

// hostModuleInstance is used to maintain the state of our module instance.
type hostModuleInstance struct {
	logger           log.CtxLogger
	commandRequests  <-chan *processorv1.CommandRequest
	commandResponses chan<- tuple[*processorv1.CommandResponse, error]

	parkedCommandRequest *processorv1.CommandRequest

	parkedCreateSchemaResponse *conduitv1.CreateSchemaResponse
	parkedCreateSchemaBuffer   []byte

	parkedGetSchemaResponse *conduitv1.GetSchemaResponse
	parkedGetSchemaBuffer   []byte
}

func (*hostModuleInstance) Close(context.Context) error { return nil }

// commandRequest is the exported function that is called by the WASM module to
// get the next command request. It returns the size of the command request
// message. If the buffer is too small, it returns the size of the command
// request message and parks the command request. The next call to this function
// will return the same command request.
func (m *hostModuleInstance) commandRequest(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing command_request")

	if m.parkedCommandRequest == nil {
		// No parked command, so we need to wait for the next one. If the command
		// channel is closed, then we return an error.
		var ok bool
		m.parkedCommandRequest, ok = <-m.commandRequests
		if !ok {
			return wasm.ErrorCodeNoMoreCommands
		}
	}

	// If the buffer is too small, we park the command and return the size of the
	// command. The next call to nextCommand will return the same command.
	if size := proto.Size(m.parkedCommandRequest); len(buf) < size {
		m.logger.Warn(ctx).
			Int("command_bytes", size).
			Int("allocated_bytes", len(buf)).
			Msgf("insufficient memory, command will be parked until next call to command_request")
		return types.Uint32(size)
	}

	// If the buffer is large enough, we marshal the command into the buffer and
	// return the size of the command. The next call to nextCommand will return
	// the next command.
	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedCommandRequest)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf command request")
		return wasm.ErrorCodeUnknownCommandRequest
	}
	m.parkedCommandRequest = nil

	m.logger.Trace(ctx).Msg("returning next command")
	return types.Uint32(len(out))
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
		return wasm.ErrorCodeUnknownCommandResponse
	}

	m.commandResponses <- tuple[*processorv1.CommandResponse, error]{&resp, nil}
	return 0
}

// createSchema is the exported function that is called by the WASM module to
// create a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success, or an error code on error.
func (m *hostModuleInstance) createSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing create_schema")

	if m.parkedCreateSchemaResponse != nil {
		if bytes.Equal(buf[:len(m.parkedCreateSchemaBuffer)], m.parkedCreateSchemaBuffer) {
			m.parkedCreateSchemaResponse = nil
			m.parkedCreateSchemaBuffer = nil
			out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedCreateSchemaResponse)
			if err != nil {
				m.logger.Err(ctx, err).Msg("failed marshalling protobuf create schema response")
				return wasm.ErrorCodeSchemaMarshal
			}
			return types.Uint32(len(out))
		}
	}

	var req conduitv1.CreateSchemaRequest
	length := len(buf)
	if m.parkedCreateSchemaBuffer != nil {
		length = len(m.parkedCreateSchemaBuffer)
	}
	err := proto.Unmarshal(buf[:length], &req)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed unmarshalling protobuf create schema request")
		return wasm.ErrorCodeSchemaUnmarshal
	}

	// use a stub response for now
	schema := schemav1.Schema{
		Bytes: []byte("CONDUIT_SCHEMA_CREATE"),
	}
	resp := pschema.CreateSchemaResponse(schema)
	m.parkedCreateSchemaResponse = resp
	m.parkedCreateSchemaBuffer = buf

	// If the buffer is too small, we park the response and return the size of the
	// response. The next call to createSchema will return the same response.
	if size := proto.Size(m.parkedCreateSchemaResponse); len(buf) < size {
		m.logger.Warn(ctx).
			Int("response_bytes", size).
			Int("allocated_bytes", len(buf)).
			Msgf("insufficient memory, response will be parked until next call to create_schema")
		return types.Uint32(size)
	}
	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedCreateSchemaResponse)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf create schema response")
		return wasm.ErrorCodeSchemaMarshal
	}
	m.parkedCreateSchemaResponse = nil
	m.parkedCreateSchemaBuffer = nil

	return types.Uint32(len(out))
}

// getSchema is the exported function that is called by the WASM module to
// get a schema and set the buffer bytes to the marshalled response.
// It returns the response size on success, or an error code on error.
func (m *hostModuleInstance) getSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	m.logger.Trace(ctx).Msg("executing get_schema")

	if m.parkedGetSchemaResponse != nil {
		if bytes.Equal(buf[:len(m.parkedGetSchemaBuffer)], m.parkedGetSchemaBuffer) {
			m.parkedGetSchemaResponse = nil
			m.parkedGetSchemaBuffer = nil
			out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedGetSchemaResponse)
			if err != nil {
				m.logger.Err(ctx, err).Msg("failed marshalling protobuf get schema response")
				return wasm.ErrorCodeSchemaMarshal
			}
			return types.Uint32(len(out))
		}
	}

	// get and unmarshal the request
	var req conduitv1.GetSchemaRequest
	length := len(buf)
	if m.parkedGetSchemaBuffer != nil {
		length = len(m.parkedGetSchemaBuffer)
	}
	err := proto.Unmarshal(buf[:length], &req)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed unmarshalling protobuf get schema request")
		return wasm.ErrorCodeSchemaUnmarshal
	}

	// todo: resp := schemaService.GetSchema(unmarshalledRequest)
	// use a stub response for now
	schema := schemav1.Schema{
		Bytes: []byte("CONDUIT_SCHEMA_GET"),
	}
	resp := pschema.GetSchemaResponse(schema)
	m.parkedGetSchemaResponse = resp
	m.parkedGetSchemaBuffer = buf

	// If the buffer is too small, we park the response and return the size of the
	// response. The next call to getSchema will return the same response.
	if size := proto.Size(m.parkedGetSchemaResponse); len(buf) < size {
		m.logger.Warn(ctx).
			Int("response_bytes", size).
			Int("allocated_bytes", len(buf)).
			Msgf("insufficient memory, response will be parked until next call to get_schema")
		return types.Uint32(size)
	}
	out, err := proto.MarshalOptions{}.MarshalAppend(buf[:0], m.parkedGetSchemaResponse)
	if err != nil {
		m.logger.Err(ctx, err).Msg("failed marshalling protobuf get schema response")
		return wasm.ErrorCodeSchemaMarshal
	}
	m.parkedGetSchemaResponse = nil
	m.parkedGetSchemaBuffer = nil

	return types.Uint32(len(out))
}
