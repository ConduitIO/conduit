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

package standalone

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/serde"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/rs/zerolog"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type wasmProcessor struct {
	logger zerolog.Logger

	runtime       wazero.Runtime
	runtimeCancel context.CancelFunc

	// commandCh is used to send commands to
	// the actual processor (the WASM module)
	// and through exported host functions.
	commandCh chan sdk.Command
	// replyErr is used to communicate replies between
	// the actual processor (the WASM module) and wasmProcessor
	replyCh chan sdk.CommandResponse
	// replyErr is used to communicate errors between
	// the actual processor (the WASM module) and wasmProcessor
	replyErr chan error
	// runModStopped is used to know when the run module stopped
	runModStopped chan struct{}
}

func NewWASMProcessor(ctx context.Context, logger zerolog.Logger, wasmPath string) (sdk.Processor, error) {
	runtimeCtx, runtimeCancel := context.WithCancel(ctx)
	r := wazero.NewRuntimeWithConfig(
		runtimeCtx,
		wazero.NewRuntimeConfig().WithCloseOnContextDone(true),
	)
	_, err := wasi_snapshot_preview1.Instantiate(ctx, r)
	if err != nil {
		runtimeCancel()
		return nil, cerrors.Errorf("failed instantiating wasi_snapshot_preview1: %w", err)
	}

	p := &wasmProcessor{
		logger:        logger,
		runtime:       r,
		runtimeCancel: runtimeCancel,
		commandCh:     make(chan sdk.Command),
		replyCh:       make(chan sdk.CommandResponse),
		replyErr:      make(chan error),
		runModStopped: make(chan struct{}),
	}

	err = p.exportFunctions(ctx)
	if err != nil {
		runtimeCancel()
		return nil, fmt.Errorf("failed exporting processor functions: %w", err)
	}

	err = p.run(ctx, wasmPath)
	if err != nil {
		runtimeCancel()
		return nil, fmt.Errorf("failed running WASM module: %w", err)
	}

	return p, nil
}

func (p *wasmProcessor) exportFunctions(ctx context.Context) error {
	// Build a host module, called `env`, which will expose
	// functions which WASM processor module can use,
	envBuilder := p.runtime.NewHostModuleBuilder("env")
	envBuilder.
		NewFunctionBuilder().
		WithFunc(p.nextCommand).
		Export("nextCommand")
	envBuilder.
		NewFunctionBuilder().
		WithFunc(p.reply).
		Export("reply")

	_, err := envBuilder.Instantiate(ctx)
	return err
}

func (p *wasmProcessor) nextCommand(ctx context.Context, m api.Module, ptr, allocSize uint32) uint32 {
	p.logger.Trace().Msg("getting next command")

	cmd, ok := <-p.commandCh
	if !ok {
		p.logger.Info().Msg("command channel closed")
		return sdk.ErrCodeNoMoreCommands
	}

	bytes, err := serde.MarshalCommand(cmd)
	if err != nil {
		p.logger.Err(err).Msg("failed marshaling command")
		p.replyErr <- fmt.Errorf("failed marshaling command: %w", err)

		return sdk.ErrCodeFailedGettingCommand
	}

	return p.write(ctx, m, ptr, allocSize, bytes)
}

func (p *wasmProcessor) write(_ context.Context, mod api.Module, ptr uint32, sizeAllocated uint32, bytes []byte) uint32 {
	p.logger.Trace().
		Int("total_bytes", len(bytes)).
		Uint32("allocated_size", sizeAllocated).
		Str("module_name", mod.Name()).
		Msgf("writing command to module memory")

	if sizeAllocated < uint32(len(bytes)) {
		p.logger.Error().
			Int("total_bytes", len(bytes)).
			Uint32("allocated_size", sizeAllocated).
			Str("module_name", mod.Name()).
			Msgf("insufficient memory")

		p.replyErr <- fmt.Errorf(
			"insufficient memory allocated for reply, needed %v, allocated %v",
			len(bytes),
			sizeAllocated,
		)
		return sdk.ErrCodeInsufficientSize
	}

	bytesWritten := uint64(len(bytes))
	// The pointer is a linear memory offset, which is where we write the name.
	if !mod.Memory().Write(ptr, bytes) {
		p.logger.Error().
			Uint32("pointer", ptr).
			Int("total_bytes", len(bytes)).
			Uint32("wasm_module_mem_size", mod.Memory().Size()).
			Msg("WASM module memory write is out of range")

		p.replyErr <- cerrors.New("WASM module memory write is out of range")
		return sdk.ErrCodeMemoryOutOfRange
	}

	p.logger.Trace().Msgf("bytes written: %v", bytesWritten)
	return uint32(bytesWritten)
}

func (p *wasmProcessor) reply(_ context.Context, m api.Module, ptr, size uint32) {
	bytes, b := m.Memory().Read(ptr, size)
	if !b {
		p.logger.Error().Msg("failed reading reply")
		p.replyErr <- cerrors.New("failed reading reply")
		return
	}

	cr, err := serde.UnmarshalCommandResponse(bytes)
	if err != nil {
		p.replyErr <- fmt.Errorf("failed deserializing command response: %w", err)
		return
	}

	p.replyCh <- cr
}

// run instantiates a new module from the given path.
// Blocks as long as the module's start function is running.
func (p *wasmProcessor) run(ctx context.Context, path string) error {
	p.logger.
		Debug().
		Str("path", path).
		Msg("running WASM processor")

	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed reading WASM file %s: %w", path, err)
	}

	// Compiling a module helps check for some errors early on
	p.logger.
		Debug().
		Str("path", path).
		Msg("compiling module")
	mod, err := p.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return fmt.Errorf("failed compiling WASM module: %w", err)
	}

	// Needs to run in a goroutine because
	// the WASM module is blocking as long as
	// the "main" function is running
	go func() {
		p.logger.Debug().Msg("instantiating module")
		_, err = p.runtime.InstantiateModule(
			ctx,
			mod,
			wazero.NewModuleConfig().
				WithName("run-module").
				WithStdout(p.logger).
				WithStderr(p.logger),
		)
		p.runModStopped <- struct{}{}
		if err != nil {
			p.logger.Err(err).Msg("WASM module not instantiated or stopped")
		}
	}()

	return nil
}

func (p *wasmProcessor) Specification() (sdk.Specification, error) {
	p.commandCh <- &sdk.SpecifyCmd{}

	select {
	case cr := <-p.replyCh:
		resp, ok := cr.(*sdk.SpecifyResponse)
		if !ok {
			return sdk.Specification{}, fmt.Errorf("unexpected response type: %T", cr)
		}

		return resp.Specification, resp.Error()
	case err := <-p.replyErr:
		return sdk.Specification{}, fmt.Errorf("reply error: %w", err)
	}
}

func (p *wasmProcessor) Configure(context.Context, map[string]string) error {
	// TODO implement me
	panic("implement me")
}

func (p *wasmProcessor) Open(context.Context) error {
	// TODO implement me
	panic("implement me")
}

func (p *wasmProcessor) Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord {
	// TODO implement me
	panic("implement me")
}

func (p *wasmProcessor) Teardown(context.Context) error {
	// Closing the command channel will send ErrCodeFailedGettingCommand
	// to the WASM module, which will exit.
	// TODO handle case when the WASM module is unresponsive.
	close(p.commandCh)
	<-p.runModStopped
	p.runtimeCancel()
	return nil
}
