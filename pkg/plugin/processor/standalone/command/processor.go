// Copyright © 2023 Meroxa, Inc.
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
	"fmt"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone/common"
	"github.com/conduitio/conduit/pkg/plugin/processor/standalone/common/fromproto"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"
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

	// commandRequests is used to send commands to the actual processor (the
	// WASM module)
	commandRequests chan *processorv1.CommandRequest
	// commandResponses is used to communicate replies between the actual
	// processor (the WASM module) and WasmProcessor
	commandResponses chan tuple[*processorv1.CommandResponse, error]

	// moduleStopped is used to know when the module stopped running
	moduleStopped chan struct{}
	// moduleError contains the error returned by the module after it stopped
	moduleError error
}

type tuple[T1, T2 any] struct {
	V1 T1
	V2 T2
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

	commandRequests := make(chan *processorv1.CommandRequest)
	commandResponses := make(chan tuple[*processorv1.CommandResponse, error])
	moduleStopped := make(chan struct{})

	// instantiate conduit host module and inject it into the context
	logger.Debug(ctx).Msg("instantiating conduit host module")
	ins, err := hostModule.Instantiate(
		ctx,
		HostModuleOptions(
			logger,
			schemaService,
			commandRequests,
			commandResponses,
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
		id:               id,
		logger:           logger,
		module:           mod,
		commandRequests:  commandRequests,
		commandResponses: commandResponses,
		moduleStopped:    moduleStopped,
	}

	// Needs to run in a goroutine because the WASM module is blocking as long
	// as the "main" function is running
	go p.run(ctx)

	return p, nil
}

// run is the main loop of the WASM module. It runs in a goroutine and blocks
// until the module is closed.
func (p *WasmProcessor) run(ctx context.Context) {
	defer close(p.moduleStopped)

	_, err := p.module.ExportedFunction("_start").Call(ctx)

	// main function returned, close the module right away
	_ = p.module.Close(ctx)

	if err != nil {
		var exitErr *sys.ExitError
		if cerrors.As(err, &exitErr) {
			if exitErr.ExitCode() == 0 { // All good
				err = nil
			}
		}
	}

	p.moduleError = err
	p.logger.Err(ctx, err).Msg("WASM module stopped")
}

func (p *WasmProcessor) Specification() (sdk.Specification, error) {
	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Specify{
			Specify: &processorv1.Specify_Request{},
		},
	}

	// the function has no context parameter, so we need to set a timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return sdk.Specification{}, err
	}

	switch specResp := resp.Response.(type) {
	case *processorv1.CommandResponse_Specify:
		return fromproto.Specification(specResp.Specify)
	default:
		return sdk.Specification{}, fmt.Errorf("unexpected response type: %T", resp)
	}
}

func (p *WasmProcessor) Configure(ctx context.Context, config config.Config) error {
	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Configure{
			Configure: &processorv1.Configure_Request{
				Parameters: config,
			},
		},
	}

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return err
	}

	switch resp.Response.(type) {
	case *processorv1.CommandResponse_Configure:
		return nil
	default:
		return fmt.Errorf("unexpected response type: %T", resp)
	}
}

func (p *WasmProcessor) Open(ctx context.Context) error {
	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Open{
			Open: &processorv1.Open_Request{},
		},
	}

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return err
	}

	switch resp.Response.(type) {
	case *processorv1.CommandResponse_Open:
		return nil
	default:
		return fmt.Errorf("unexpected response type: %T", resp)
	}
}

func (p *WasmProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	protoRecords, err := fromproto.Records(records)
	if err != nil {
		p.logger.Err(ctx, err).Msg("failed to convert records to proto")
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}

	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Process{
			Process: &processorv1.Process_Request{
				Records: protoRecords,
			},
		},
	}

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}

	switch procResp := resp.Response.(type) {
	case *processorv1.CommandResponse_Process:
		processedRecords, err := fromproto.ProcessedRecords(procResp.Process.Records)
		if err != nil {
			p.logger.Err(ctx, err).Msg("failed to convert processed records from proto")
			return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
		}
		return processedRecords
	default:
		err := fmt.Errorf("unexpected response type: %T", resp)
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: err}}
	}
}

func (p *WasmProcessor) Teardown(ctx context.Context) error {
	// TODO: we should probably have a timeout for the teardown command in case
	//  the plugin is stuck
	teardownErr := p.executeTeardownCommand(ctx)
	// close module regardless of teardown error
	stopErr := p.closeModule(ctx)

	return cerrors.Join(teardownErr, stopErr, p.moduleError)
}

func (p *WasmProcessor) executeTeardownCommand(ctx context.Context) error {
	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Teardown{
			Teardown: &processorv1.Teardown_Request{},
		},
	}

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return err
	}
	switch resp.Response.(type) {
	case *processorv1.CommandResponse_Teardown:
		return nil
	default:
		return fmt.Errorf("unexpected response type: %T", resp)
	}
}

func (p *WasmProcessor) closeModule(ctx context.Context) error {
	// Closing the command channel will send an error code to the WASM module
	// signaling it to exit.
	close(p.commandRequests)

	select {
	case <-ctx.Done():
		// kill the plugin
		p.logger.Error(ctx).Msg("context canceled while waiting for teardown, killing plugin")
		err := p.module.CloseWithExitCode(ctx, 1)
		if err != nil {
			return fmt.Errorf("failed to kill processor plugin: %w", err)
		}
		return ctx.Err()
	case <-p.moduleStopped:
		return nil
	}
}

// executeCommand sends a command request to the WASM module and waits for the
// response. It returns the response, or an error if the response is an error.
// If the context is canceled, it returns ctx.Err().
func (p *WasmProcessor) executeCommand(ctx context.Context, req *processorv1.CommandRequest) (*processorv1.CommandResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.moduleStopped:
		return nil, cerrors.Errorf("processor plugin stopped while trying to send command %T: %w", req.Request, plugin.ErrPluginNotRunning)
	case p.commandRequests <- req:
	}

	// wait for the response from the WASM module
	var resp *processorv1.CommandResponse
	var err error
	select {
	case <-ctx.Done():
		// TODO if this happens we should probably kill the plugin, as it's
		//  probably stuck
		return nil, ctx.Err()
	case <-p.moduleStopped:
		return nil, cerrors.Errorf("processor plugin stopped while waiting for response to command %T: %w", req.Request, plugin.ErrPluginNotRunning)
	case crTuple := <-p.commandResponses:
		resp, err = crTuple.V1, crTuple.V2
	}

	if err != nil {
		return nil, err
	}

	// check if the response is an error
	if errResp, ok := resp.Response.(*processorv1.CommandResponse_Error); ok {
		return nil, fromproto.Error(errResp.Error)
	}

	return resp, nil
}
