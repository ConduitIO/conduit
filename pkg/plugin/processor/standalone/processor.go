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
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
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

// Bounds on the lifecycle commands that have no natural caller deadline. They
// are variables only so tests can shrink them; nothing outside tests writes them.
var (
	// specificationTimeout bounds Specification(), whose signature carries no
	// context. It used to be 5s, which a -race build running the interpreter
	// under CI load exceeded (#2981); the point of the bound is to turn a stuck
	// guest into an error, not to police a slow-but-working one.
	specificationTimeout = 30 * time.Second
	// teardownCommandTimeout bounds the Teardown command, so a guest that never
	// answers cannot stall shutdown forever.
	teardownCommandTimeout = 5 * time.Second
	// moduleStopTimeout bounds how long Teardown waits for the module to exit
	// after the command channel is closed, before it kills the module.
	moduleStopTimeout = 5 * time.Second
)

// ErrCommandTimeout is wrapped by errors from a command that did not complete
// because its context ended (deadline or cancellation; the wrapped ctx.Err()
// says which) or because the plugin had to be killed for not stopping. After a
// command was abandoned mid-flight the processor is unusable (see
// wasmProcessor.abandon), so callers should tear it down.
var ErrCommandTimeout = cerrors.New("processor plugin command did not complete")

type wasmProcessor struct {
	sdk.UnimplementedProcessor
	protoconv protoConverter

	id     string
	logger log.CtxLogger

	// module is the WASM module that implements the processor
	module api.Module

	// commandRequests is used to send commands to the actual processor (the
	// WASM module)
	commandRequests chan *processorv1.CommandRequest
	// commandResponses is used to communicate replies between the actual
	// processor (the WASM module) and wasmProcessor
	commandResponses chan tuple[*processorv1.CommandResponse, error]

	// abandoned is closed (once, via abandonOnce) when executeCommand gives up on
	// a command whose request the guest already accepted. The guest will still
	// try to deliver that command's response; abandoned lets the host module drop
	// it instead of blocking forever, and makes every later command fail fast so
	// the stale response can never be paired with a newer request (the protocol
	// carries no correlation id).
	abandoned   chan struct{}
	abandonOnce sync.Once

	// moduleStopped is used to know when the module stopped running
	moduleStopped chan struct{}
	// moduleError contains the error returned by the module after it stopped
	moduleError error
}

type tuple[T1, T2 any] struct {
	V1 T1
	V2 T2
}

func newWASMProcessor(
	ctx context.Context,

	runtime wazero.Runtime,
	processorModule wazero.CompiledModule,
	hostModule *wazergo.CompiledModule[*hostModuleInstance],
	schemaService pprocutils.SchemaService,
	egressPolicy egress.Policy,

	id string,
	logger log.CtxLogger,
) (*wasmProcessor, error) {
	logger = logger.WithComponent("standalone.wasmProcessor")
	logger.Logger = logger.With().Str(log.ProcessorIDField, id).Logger()
	wasmLogger := newWasmLogWriter(logger, processorModule)

	commandRequests := make(chan *processorv1.CommandRequest)
	commandResponses := make(chan tuple[*processorv1.CommandResponse, error])
	moduleStopped := make(chan struct{})
	abandoned := make(chan struct{})

	// Build the per-processor egress capability from the resolved policy. The
	// policy is bound here, per instance — never on the shared Registry — so one
	// processor's allowlist cannot leak into another's (a tested isolation
	// invariant). A deny-all policy still yields a Service that refuses every
	// call with ErrHTTPEgressDisabled.
	//
	// The secret resolver is host-side and namespaced (CONDUIT_SECRET_*): it is
	// safe to wire unconditionally because it is only consulted for an
	// AuthSecretRef the per-processor policy already granted, and only on an
	// enabled policy (a deny-all Do never reaches the credential path). The
	// resolver is stateless process-env, so a single value serves every
	// processor; per-processor scope is enforced by the policy's granted refs.
	httpService := egress.New(egressPolicy, logger, egress.WithSecretResolver(egress.EnvSecretResolver{}))

	// instantiate conduit host module and inject it into the context
	logger.Debug(ctx).Msg("instantiating conduit host module")
	ins, err := hostModule.Instantiate(
		ctx,
		hostModuleOptions(
			logger,
			commandRequests,
			commandResponses,
			abandoned,
			schemaService,
			httpService,
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

	p := &wasmProcessor{
		id:               id,
		logger:           logger,
		module:           mod,
		commandRequests:  commandRequests,
		commandResponses: commandResponses,
		abandoned:        abandoned,
		moduleStopped:    moduleStopped,
	}

	// Needs to run in a goroutine because the WASM module is blocking as long
	// as the "main" function is running
	go p.run(ctx)

	return p, nil
}

// run is the main loop of the WASM module. It runs in a goroutine and blocks
// until the module is closed.
func (p *wasmProcessor) run(ctx context.Context) {
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

func (p *wasmProcessor) Specification() (sdk.Specification, error) {
	return p.specification(context.Background())
}

// specification is Specification with a caller context: the command is bounded
// by whichever expires first, ctx or specificationTimeout.
func (p *wasmProcessor) specification(ctx context.Context) (sdk.Specification, error) {
	req := &processorv1.CommandRequest{
		Request: &processorv1.CommandRequest_Specify{
			Specify: &processorv1.Specify_Request{},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, specificationTimeout)
	defer cancel()

	resp, err := p.executeCommand(ctx, req)
	if err != nil {
		return sdk.Specification{}, err
	}

	switch specResp := resp.Response.(type) {
	case *processorv1.CommandResponse_Specify:
		return p.protoconv.specification(specResp.Specify)
	default:
		return sdk.Specification{}, fmt.Errorf("unexpected response type: %T", resp)
	}
}

func (p *wasmProcessor) Configure(ctx context.Context, config config.Config) error {
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

func (p *wasmProcessor) Open(ctx context.Context) error {
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

func (p *wasmProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	protoRecords, err := p.protoconv.records(records)
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
		processedRecords, err := p.protoconv.processedRecords(procResp.Process.Records)
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

func (p *wasmProcessor) Teardown(ctx context.Context) error {
	var teardownErr error
	if p.isAbandoned() {
		// The guest is still busy with (or stuck on) a command we stopped waiting
		// for, so it cannot service a Teardown command; closeModule below still
		// asks it to exit and kills it if it does not.
		teardownErr = cerrors.Errorf("skipping teardown command: %w", ErrCommandTimeout)
	} else {
		tctx, cancel := context.WithTimeout(ctx, teardownCommandTimeout)
		teardownErr = p.executeTeardownCommand(tctx)
		cancel()
	}
	// close module regardless of teardown error
	stopErr := p.closeModule(ctx)

	// moduleError is written by run before it closes moduleStopped; only read it
	// once that has happened (closeModule may have given up and killed the module).
	var moduleErr error
	select {
	case <-p.moduleStopped:
		moduleErr = p.moduleError
	default:
	}

	return cerrors.Join(teardownErr, stopErr, moduleErr)
}

func (p *wasmProcessor) executeTeardownCommand(ctx context.Context) error {
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

func (p *wasmProcessor) closeModule(ctx context.Context) error {
	// Closing the command channel will send an error code to the WASM module
	// signaling it to exit.
	close(p.commandRequests)

	timer := time.NewTimer(moduleStopTimeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		// The guest neither answered nor exited (e.g. it is stuck inside a
		// command). Kill it rather than wait forever. ctx may already be
		// cancelled, so the kill must not depend on it.
		p.logger.Error(ctx).Msg("processor plugin did not stop in time, killing plugin")
		if err := p.module.CloseWithExitCode(context.WithoutCancel(ctx), 1); err != nil {
			return fmt.Errorf("failed to kill processor plugin: %w", err)
		}
		return fmt.Errorf("processor plugin did not stop within %v: %w", moduleStopTimeout, ErrCommandTimeout)
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
// If the context is done, it returns an error wrapping both ctx.Err() and
// ErrCommandTimeout.
//
// If ctx ends AFTER the guest accepted the request, the command is abandoned:
// the guest keeps running it and will try to deliver a response nobody reads.
// The processor is marked unusable (see abandon) and later commands fail fast
// rather than risk pairing that stale response with a newer request.
func (p *wasmProcessor) executeCommand(ctx context.Context, req *processorv1.CommandRequest) (*processorv1.CommandResponse, error) {
	select {
	case <-p.abandoned:
		return nil, p.abandonedError(req)
	default:
	}

	select {
	case <-ctx.Done():
		// Nothing was sent, so nothing is outstanding: not an abandonment.
		return nil, fmt.Errorf("%w while sending command %T: %w", ErrCommandTimeout, req.Request, ctx.Err())
	case <-p.moduleStopped:
		return nil, cerrors.Errorf("processor plugin stopped while trying to send command %T: %w", req.Request, plugin.ErrPluginNotRunning)
	case p.commandRequests <- req:
	}

	// wait for the response from the WASM module
	var resp *processorv1.CommandResponse
	var err error
	select {
	case <-ctx.Done():
		// The guest is still working on req and will want to send a response. Kill
		// is left to Teardown; here we only make sure nothing blocks on it.
		p.abandon()
		return nil, fmt.Errorf("%w while waiting for response to command %T: %w", ErrCommandTimeout, req.Request, ctx.Err())
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
		return nil, p.protoconv.error(errResp.Error)
	}

	return resp, nil
}

// abandon records that a command was given up on. It is idempotent.
func (p *wasmProcessor) abandon() {
	p.abandonOnce.Do(func() { close(p.abandoned) })
}

func (p *wasmProcessor) isAbandoned() bool {
	select {
	case <-p.abandoned:
		return true
	default:
		return false
	}
}

func (p *wasmProcessor) abandonedError(req *processorv1.CommandRequest) error {
	return cerrors.Errorf("refusing command %T: an earlier command was abandoned and the plugin must be torn down: %w", req.Request, ErrCommandTimeout)
}
