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

package reactor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone/common"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone/common/fromproto"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"google.golang.org/protobuf/proto"
)

const (
	// magicCookieKey and value are used as a very basic verification
	// that a plugin is intended to be launched. This is not a security
	// measure, just a UX feature. If the magic cookie doesn't match,
	// we show human-friendly output.
	magicCookieKey   = "CONDUIT_MAGIC_COOKIE"
	magicCookieValue = "3stnegqd0x02axggy0vrc4izjeq2zik6g7somyb3ye4vy5iivvjm5s1edppl5oja"

	conduitProcessorIDKey = "CONDUIT_PROCESSOR_ID"
	conduitLogLevelKey    = "CONDUIT_LOG_LEVEL"
)

type WasmProcessor struct {
	sdk.UnimplementedProcessor

	id     string
	logger log.CtxLogger

	// module is the WASM module that implements the processor
	module api.Module

	mallocFn api.Function

	specificationFn api.Function
	configureFn     api.Function
	openFn          api.Function
	processFn       api.Function
	teardownFn      api.Function

	m sync.Mutex
	// buf is the buffer used to communicate with the WASM module.
	buf []byte
	// lastMemorySize is the size of the memory when we last allocated the
	// buffer in the WASM module. This is used to detect if the memory has grown,
	// and the pointer might be invalidated.
	lastMemorySize uint32
	// modulePointer is the pointer to the buffer in the WASM module. It is used
	// to write data to the WASM module.
	modulePointer uint32
}

func NewWasmProcessor(
	ctx context.Context,

	runtime wazero.Runtime,
	processorModule wazero.CompiledModule,
	hostModule *wazergo.CompiledModule[*HostModuleInstance],
	schemaService pprocutils.SchemaService,

	id string,
	logger log.CtxLogger,
) (*WasmProcessor, error) {
	logger = logger.WithComponentFromType(WasmProcessor{})
	logger.Logger = logger.With().Str(log.ProcessorIDField, id).Logger()
	wasmLogger := common.NewWasmLogWriter(logger, processorModule)

	// instantiate conduit host module and inject it into the context
	logger.Debug(ctx).Msg("instantiating conduit host module")
	ins, err := hostModule.Instantiate(
		ctx,
		HostModuleOptions(
			logger,
			schemaService,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate conduit host module: %w", err)
	}
	ctx = wazergo.WithModuleInstance(ctx, ins)

	logger.Debug(ctx).Msg("instantiating processor module")
	mod, err := runtime.InstantiateModule(
		ctx,
		processorModule,
		wazero.NewModuleConfig().
			WithName(id). // ensure unique module name
			WithEnv(magicCookieKey, magicCookieValue).
			WithEnv(conduitProcessorIDKey, id).
			WithEnv(conduitLogLevelKey, logger.GetLevel().String()).

			// set up logging
			WithStdout(wasmLogger).
			WithStderr(wasmLogger).

			// enable time.Now to include correct wall time
			WithSysWalltime().
			// enable time.Now to include correct monotonic time
			WithSysNanotime().
			// enable time.Sleep to sleep for the correct amount of time
			WithSysNanosleep().

			// don't start right away
			WithStartFunctions(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate processor module: %w", err)
	}

	p := &WasmProcessor{
		id:     id,
		logger: logger,
		module: mod,
	}

	err = p.init(ctx)
	if err != nil {
		_ = p.module.Close(ctx) // close the module if initialization failed
		return nil, fmt.Errorf("failed to initialize processor module: %w", err)
	}

	return p, nil
}

// run is the main loop of the WASM module. It runs in a goroutine and blocks
// until the module is closed.
func (p *WasmProcessor) init(ctx context.Context) error {
	_, err := p.module.ExportedFunction("_initialize").Call(ctx)
	if err != nil {
		return cerrors.Errorf("failed to initialize processor Wasm module: %w", err)
	}

	if p.mallocFn, err = getExportedFunction(p.module, mallocFunctionDefinition); err != nil {
		return err
	}
	if p.specificationFn, err = getExportedFunction(p.module, specificationFunctionDefinition); err != nil {
		return err
	}
	if p.configureFn, err = getExportedFunction(p.module, configureFunctionDefinition); err != nil {
		return err
	}
	if p.openFn, err = getExportedFunction(p.module, openFunctionDefinition); err != nil {
		return err
	}
	if p.processFn, err = getExportedFunction(p.module, processFunctionDefinition); err != nil {
		return err
	}
	if p.teardownFn, err = getExportedFunction(p.module, teardownFunctionDefinition); err != nil {
		return err
	}

	return nil
}

func (p *WasmProcessor) Specification() (sdk.Specification, error) {
	var req processorv1.Specify_Request
	var resp processorv1.Specify_Response

	// the function has no context parameter, so we need to set a timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	err := p.executeCall(ctx, &req, &resp, p.specificationFn)
	if err != nil {
		return sdk.Specification{}, fmt.Errorf("failed to execute specification command: %w", err)
	}
	return fromproto.Specification(&resp)
}

func (p *WasmProcessor) Configure(ctx context.Context, config config.Config) error {
	var req processorv1.Configure_Request
	var resp processorv1.Configure_Response

	req.Parameters = config

	err := p.executeCall(ctx, &req, &resp, p.configureFn)
	if err != nil {
		return fmt.Errorf("failed to execute configure command: %w", err)
	}
	return nil
}

func (p *WasmProcessor) Open(ctx context.Context) error {
	var req processorv1.Open_Request
	var resp processorv1.Open_Response

	err := p.executeCall(ctx, &req, &resp, p.openFn)
	if err != nil {
		return fmt.Errorf("failed to execute open command: %w", err)
	}
	return nil
}

func (p *WasmProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	var req processorv1.Process_Request
	var resp processorv1.Process_Response

	// Convert the records to the protobuf format expected by the WASM module.
	protoRecords, err := fromproto.Records(records)
	if err != nil {
		p.logger.Err(ctx, err).Msg("failed to convert records to proto")
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}

	req.Records = protoRecords

	err = p.executeCall(ctx, &req, &resp, p.processFn)
	if err != nil {
		p.logger.Err(ctx, err).Msg("failed to execute process command")
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}

	// Convert the processed records back to the SDK format.
	processedRecords, err := fromproto.ProcessedRecords(resp.Records)
	if err != nil {
		p.logger.Err(ctx, err).Msg("failed to convert processed records from proto")
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}
	return processedRecords
}

func (p *WasmProcessor) Teardown(ctx context.Context) error {
	// TODO: we should probably have a timeout for the teardown command in case
	//  the plugin is stuck
	teardownErr := p.executeTeardownCommand(ctx)

	// close module regardless of teardown error
	closeErr := p.module.CloseWithExitCode(ctx, 0)

	return cerrors.Join(teardownErr, closeErr)
}

func (p *WasmProcessor) executeTeardownCommand(ctx context.Context) error {
	var req processorv1.Teardown_Request
	var resp processorv1.Teardown_Response

	// Execute the teardown command in the WASM module.
	err := p.executeCall(ctx, &req, &resp, p.teardownFn)
	if err != nil {
		p.logger.Err(ctx, err).Msg("failed to execute teardown command")
		return fmt.Errorf("failed to execute teardown command: %w", err)
	}
	return nil
}

// executeCall executes a function call to the WASM module, ensuring it allocates
// enough memory and collects the response. It returns the response, or an error
// if the response is an error.
func (p *WasmProcessor) executeCall(
	ctx context.Context,
	req proto.Message,
	resp proto.Message,
	fn api.Function,
) error {
	p.m.Lock()
	defer p.m.Unlock()

	logger := p.logger.With().Ctx(ctx).Str("function", fn.Definition().Name()).Logger()

	// Step 1: Allocate memory in the Wasm module if needed.
	if msgSize := proto.Size(req); cap(p.buf) < msgSize ||
		p.lastMemorySize != p.module.Memory().Size() {
		logger.Debug().Msg("memory buffer is too small or memory size has changed, reallocating using malloc function")

		results, err := p.mallocFn.Call(ctx, api.EncodeI32(int32(msgSize)))
		if err != nil {
			logger.Err(err).Msg("failed to call Wasm function")
			return cerrors.Errorf("failed to call Wasm function %q: %w", p.mallocFn.Definition().Name(), err)
		}
		p.modulePointer = api.DecodeU32(results[0])
		p.lastMemorySize = p.module.Memory().Size()

		if cap(p.buf) < msgSize {
			p.buf = make([]byte, msgSize)
		}
	}

	// Step 2: Marshal the request into the buffer.
	reqBytes, err := proto.MarshalOptions{}.MarshalAppend(p.buf[:0], req)
	if err != nil {
		logger.Err(err).Msg("failed marshalling protobuf command request")
		return cerrors.Errorf("failed to marshal protobuf command request: %w", err)
	}

	// Step 3: Write the request to the Wasm module's memory.
	if !p.module.Memory().Write(p.modulePointer, reqBytes) {
		logger.Error().
			Uint32("ptr", p.modulePointer).
			Int("size", len(reqBytes)).
			Msg("failed to write to Wasm module memory")
		return cerrors.Errorf("failed to write to Wasm module memory at pointer %d with size %d", p.modulePointer, len(reqBytes))
	}

	// Step 4: Call the Wasm function with the pointer and size of the buffer.
	results, err := fn.Call(
		ctx,
		api.EncodeU32(p.modulePointer),
		api.EncodeU32(uint32(len(reqBytes))),
	)
	if err != nil {
		logger.Err(err).Msg("failed to call Wasm function")
		return cerrors.Errorf("failed to call Wasm function %q: %w", fn.Definition().Name(), err)
	}

	// Step 5: Check the results of the function call.
	ptrSize := results[0]
	ptr := uint32(ptrSize >> 32)
	size := uint32(ptrSize)

	// Read the byte slice from the module's memory.
	respBytes, ok := p.module.Memory().Read(ptr, size)
	if !ok {
		logger.Error().
			Uint32("ptr", ptr).
			Uint32("size", size).
			Msg("failed to read from Wasm module memory")
		return cerrors.Errorf("failed to read from Wasm module memory at pointer %d with size %d", ptr, size)
	}

	if err := proto.Unmarshal(respBytes, resp); err != nil {
		logger.Err(err).Msg("failed to unmarshal protobuf command response")
		return cerrors.Errorf("failed to unmarshal protobuf command response: %w", err)
	}

	// Check if the response is an error
	// TODO
	// if errResp, ok := resp.Response.(*processorv1.CommandResponse_Error); ok {
	// 	return nil, fromproto.Error(errResp.Error)
	// }

	return nil
}
