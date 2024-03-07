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
	"context"

	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
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
