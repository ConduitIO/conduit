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

package upgrade

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// seqPlugin is a purpose-built, in-process pconnector.SourcePlugin for the
// batch-shape upgrade-coverage suite. It is deliberately NOT
// conduit-connector-generator: generator's Source.Open discards its resume
// position argument entirely (see the package doc), so any resume assertion
// built on it passes vacuously no matter what the engine actually did.
// seqPlugin genuinely honors the resume position - Open seeks to the record
// immediately after it, exactly like a real connector must (e.g.
// builtin:file's Source.Open, which seeks a real byte offset - see
// conduit-connector-file@v0.10.3/source.go).
//
// Records are a fixed, pre-built, 1-indexed sequence ("1".."N"). On each Run,
// the entire remaining sequence (from the resume position onward) is handed
// to the stream in ONE Send call. builtin.InMemorySourceRunStream is an
// unbuffered rendezvous (see stream.go's inMemoryStreamServer.Send /
// inMemoryStreamClient.Recv), so one Send is deterministically one
// pkg/connector.Source.Read() call, which is deterministically one
// funnel.SourceTask.Do() batch (source.go:85-98: "Overwrite the batch with
// the new records"). That is what lets a test control the exact batch SHAPE
// (which positions land in the one batch under test) just by choosing how
// many records seqPlugin was built with - no pacing, no timing, no
// goroutine-scheduling dependence.
//
// The actual shape under test (filter/retry/nack/split/fan-out) is entirely
// the job of the funnel.Task / stream.Processor the test wires downstream of
// this source - seqPlugin only ever produces plain, unmarked records.
type seqPlugin struct {
	records []opencdc.Record

	mu      sync.Mutex
	nextIdx int // 0-based index of the next record to deliver; set by Open
}

// newSeqPlugin builds a seqPlugin with n records at positions "1".."n" (in
// that order). Positions are 1-based so that a persisted position of 0
// unambiguously means "nothing acked yet".
func newSeqPlugin(n int) *seqPlugin {
	records := make([]opencdc.Record, n)
	for i := range records {
		pos := i + 1
		records[i] = opencdc.Record{
			Position:  encodeSeqPosition(pos),
			Operation: opencdc.OperationCreate,
			Metadata:  opencdc.Metadata{},
			Key:       opencdc.RawData(fmt.Sprintf("key-%d", pos)),
			Payload:   opencdc.Change{After: opencdc.RawData(fmt.Sprintf("record-%d", pos))},
		}
	}
	return &seqPlugin{records: records}
}

var _ pconnector.SourcePlugin = (*seqPlugin)(nil)

func (p *seqPlugin) Configure(context.Context, pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	return pconnector.SourceConfigureResponse{}, nil
}

// Open is the genuine-resume enforcement site (see the type doc): it decodes
// the requested resume position and sets nextIdx accordingly, so a restarted
// run (see sourceHarness.restart) only ever redelivers records that were
// never durably acked - never records 1..resume, and never skipping anything
// after resume either.
func (p *seqPlugin) Open(_ context.Context, req pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	resume, err := decodeSeqPosition(req.Position)
	if err != nil {
		return pconnector.SourceOpenResponse{}, fmt.Errorf("seqPlugin: invalid resume position %q: %w", req.Position, err)
	}
	p.mu.Lock()
	p.nextIdx = resume // resume is the count of already-acked records; 0 == fresh start
	p.mu.Unlock()
	return pconnector.SourceOpenResponse{}, nil
}

func (p *seqPlugin) Run(ctx context.Context, stream pconnector.SourceRunStream) error {
	inmemStream, ok := stream.(*builtin.InMemorySourceRunStream)
	if !ok {
		return fmt.Errorf("seqPlugin: unexpected stream type %T", stream)
	}
	inmemStream.Init(ctx)
	server := inmemStream.Server()

	p.mu.Lock()
	start := p.nextIdx
	p.mu.Unlock()

	go func() {
		if start < len(p.records) {
			// One Send == one batch, by construction - see the type doc.
			remaining := append([]opencdc.Record(nil), p.records[start:]...)
			_ = server.Send(pconnector.SourceRunResponse{Records: remaining})
		}
		<-ctx.Done() // nothing more to produce in this run; park until torn down
	}()
	go func() {
		// Drain acks so Source's deferred-ack delivery goroutine never blocks
		// on a full send. The durable resume position is read back
		// independently, straight from the badger store (see
		// sourceHarness.persistedPosition) - never from what this plugin
		// observes - so nothing here needs to inspect the ack payload.
		for {
			if _, err := server.Recv(); err != nil {
				return
			}
		}
	}()
	return nil
}

// Stop reports the position of the LAST record this connector run will ever
// produce - the real semantics pkg/lifecycle/stream/source.go's
// SourceNode.stopGraceful relies on to know when to stop forwarding records
// (it injects this as a control message and keeps draining until it sees a
// record with this exact position go by).
func (p *seqPlugin) Stop(context.Context, pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
	if len(p.records) == 0 {
		return pconnector.SourceStopResponse{}, nil
	}
	return pconnector.SourceStopResponse{LastPosition: p.records[len(p.records)-1].Position}, nil
}

func (p *seqPlugin) Teardown(context.Context, pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	return pconnector.SourceTeardownResponse{}, nil
}

func (p *seqPlugin) LifecycleOnCreated(context.Context, pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	return pconnector.SourceLifecycleOnCreatedResponse{}, nil
}

func (p *seqPlugin) LifecycleOnUpdated(context.Context, pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	return pconnector.SourceLifecycleOnUpdatedResponse{}, nil
}

func (p *seqPlugin) LifecycleOnDeleted(context.Context, pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	return pconnector.SourceLifecycleOnDeletedResponse{}, nil
}

func (p *seqPlugin) NewStream() pconnector.SourceRunStream {
	return &builtin.InMemorySourceRunStream{}
}

func encodeSeqPosition(n int) opencdc.Position {
	return opencdc.Position(strconv.Itoa(n))
}

// decodeSeqPosition returns 0 for a nil/empty position (a genuinely fresh
// start - no state persisted yet), matching connector.Source's own
// zero-value SourceState.
func decodeSeqPosition(p opencdc.Position) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return strconv.Atoi(string(p))
}

// staticDispenser and staticFetcher wire a single, already-constructed
// seqPlugin into pkg/connector's plugin-dispensing machinery
// (connector.PluginDispenserFetcher -> connectorPlugin.Dispenser ->
// connectorPlugin.SourcePlugin). Mirrors tests/chaos/child.go's identical
// helpers (and, before that, pkg/connector/instance_test.go's
// fakePluginFetcher) - duplicated here rather than imported because
// tests/chaos is an unrelated internal test package with no exported surface.
type staticDispenser struct {
	source connectorPlugin.SourcePlugin
}

func (d staticDispenser) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("staticDispenser: DispenseSpecifier not implemented")
}

func (d staticDispenser) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	return d.source, nil
}

func (d staticDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return nil, cerrors.New("staticDispenser: DispenseDestination not implemented")
}

type staticFetcher map[string]connectorPlugin.Dispenser

func (f staticFetcher) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	d, ok := f[name]
	if !ok {
		return nil, cerrors.Errorf("staticFetcher: no dispenser registered for plugin %q", name)
	}
	return d, nil
}
