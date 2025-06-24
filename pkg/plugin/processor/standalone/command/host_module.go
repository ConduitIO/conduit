// Copyright © 2025 Meroxa, Inc.
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

package command

import (
	"context"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone/common"
	"github.com/stealthrocket/wazergo"
	"github.com/stealthrocket/wazergo/types"
	"google.golang.org/protobuf/proto"
)

// HostModule declares the host module that is exported to the WASM module. The
// host module is used to communicate between the WASM module (processor) and Conduit.
var HostModule wazergo.HostModule[*HostModuleInstance] = HostModuleFunctions{
	"command_request":  wazergo.F1((*HostModuleInstance).CommandRequest),
	"command_response": wazergo.F1((*HostModuleInstance).CommandResponse),
	"create_schema":    wazergo.F1((*HostModuleInstance).CreateSchema),
	"get_schema":       wazergo.F1((*HostModuleInstance).GetSchema),
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
	requests <-chan *processorv1.CommandRequest,
	responses chan<- tuple[*processorv1.CommandResponse, error],
) HostModuleOption {
	return wazergo.OptionFunc(func(m *HostModuleInstance) {
		wazergo.Configure(m.HostModuleInstance, common.HostModuleOptions(logger, schemaService))
		m.logger = logger
		m.commandRequests = requests
		m.commandResponses = responses
	})
}

// HostModuleInstance is used to maintain the state of our module instance.
type HostModuleInstance struct {
	*common.HostModuleInstance

	logger               log.CtxLogger
	commandRequests      <-chan *processorv1.CommandRequest
	commandResponses     chan<- tuple[*processorv1.CommandResponse, error]
	parkedCommandRequest *processorv1.CommandRequest
}

func NewHostModuleInstance() *HostModuleInstance {
	return &HostModuleInstance{
		HostModuleInstance: common.NewHostModuleInstance(),
	}
}

func (m *HostModuleInstance) CreateSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	return m.HostModuleInstance.CreateSchema(ctx, buf)
}

func (m *HostModuleInstance) GetSchema(ctx context.Context, buf types.Bytes) types.Uint32 {
	return m.HostModuleInstance.GetSchema(ctx, buf)
}

// CommandRequest is the exported function that is called by the WASM module to
// get the next command request. It returns the size of the command request
// message. If the buffer is too small, it returns the size of the command
// request message and parks the command request. The next call to this function
// will return the same command request.
func (m *HostModuleInstance) CommandRequest(ctx context.Context, buf types.Bytes) types.Uint32 {
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
		return types.Uint32(size) //nolint:gosec // no risk of overflow
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
	return types.Uint32(len(out)) //nolint:gosec // no risk of overflow
}

// CommandResponse is the exported function that is called by the WASM module to
// send a command response. It returns 0 on success, or an error code on error.
func (m *HostModuleInstance) CommandResponse(ctx context.Context, buf types.Bytes) types.Uint32 {
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
