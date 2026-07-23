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

package funnel

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	pconnectorv1client "github.com/conduitio/conduit-connector-protocol/pconnector/v1/client" //nolint:staticcheck // intentionally testing the v1 client: conduit-kafka-connect-wrapper (and other standalone connectors) still use it today
	connectorv1 "github.com/conduitio/conduit-connector-protocol/proto/connector/v1"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// This file closes issue #2672: the engine sends multi-position Source.Ack
// batches (funnel/worker.go:493 for a full-batch ack, funnel/worker.go:513 for
// the partial-batch ack that follows a partial DLQ nack), and a v1-protocol
// (out-of-process, standalone-connector-shaped) plugin — like
// ConduitIO/conduit-kafka-connect-wrapper — depends on receiving those
// positions as strictly ordered, individual messages, one per position. Before
// this test, that ordering guarantee was upheld only by a comment in
// conduit-connector-protocol's v1 client ("Batching is not supported in v1,
// send each request individually", pconnector/v1/client/source.go:148) — a
// third-party implementation detail, not anything asserted in this repo. If a
// future change to that dependency (or to pkg/connector.Source.Ack itself)
// ever reordered, batched, or dropped positions before delivery, nothing here
// would catch it before every standalone connector's ack-ordering assumption
// broke in production.
//
// To pin the contract at the boundary Conduit's own engine controls, these
// tests exercise the REAL production code path end to end: funnel.Worker.Ack /
// funnel.Worker.Nack -> pkg/connector.Source.Ack -> the real
// conduit-connector-protocol v1 gRPC client (not a mock of it) -> a real gRPC
// bidirectional stream (over an in-memory bufconn listener, so no network or
// subprocess is needed) -> a fake v1 SourcePluginServer that records the
// arrival order of ack positions. This is deliberately NOT a test of
// pkg/connector.Source.Ack in isolation with a mocked stream (as
// TestSource_Ack_Deadlock in pkg/connector/source_test.go already does for a
// different property) — it is a test of what a v1-protocol plugin actually
// observes on the wire, using the exact client library standalone connectors
// use today.

// fakeV1SourceServer is a minimal, real gRPC server implementing the v1
// SourcePluginServer service. It only needs to support what Source.Open and
// Source.Ack exercise: Configure, LifecycleOnCreated ("created" lifecycle
// event, since this is the connector's first run), Start, and Run (the
// bidirectional ack/record stream). Everything else embeds
// UnimplementedSourcePluginServer and is never called by these tests.
type fakeV1SourceServer struct {
	connectorv1.UnimplementedSourcePluginServer

	mu   sync.Mutex
	acks []opencdc.Position
}

func (s *fakeV1SourceServer) Configure(context.Context, *connectorv1.Source_Configure_Request) (*connectorv1.Source_Configure_Response, error) {
	return &connectorv1.Source_Configure_Response{}, nil
}

func (s *fakeV1SourceServer) LifecycleOnCreated(context.Context, *connectorv1.Source_Lifecycle_OnCreated_Request) (*connectorv1.Source_Lifecycle_OnCreated_Response, error) {
	return &connectorv1.Source_Lifecycle_OnCreated_Response{}, nil
}

func (s *fakeV1SourceServer) Start(context.Context, *connectorv1.Source_Start_Request) (*connectorv1.Source_Start_Response, error) {
	return &connectorv1.Source_Start_Response{}, nil
}

// Run mirrors what the wrapper's DefaultSourceStream.onNext does on receiving
// an ack message: it observes exactly one AckPosition per message. It records
// the arrival order and never batches multiple positions into one Recv.
func (s *fakeV1SourceServer) Run(stream connectorv1.SourcePlugin_RunServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			// Client closed the stream (io.EOF) or the context was canceled
			// during test cleanup - nothing left to observe.
			return nil //nolint:nilerr // stream teardown, not a test failure
		}

		s.mu.Lock()
		// Clone the position: req is only valid until the next Recv call, and
		// the underlying byte slice must not be aliased across appends.
		s.acks = append(s.acks, append(opencdc.Position(nil), req.AckPosition...))
		s.mu.Unlock()
	}
}

func (s *fakeV1SourceServer) observedAcks() []opencdc.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]opencdc.Position(nil), s.acks...)
}

// fakeSourceDispenser implements connectorPlugin.Dispenser and always
// dispenses the same pre-built v1 client-backed SourcePlugin. Only
// DispenseSource is exercised by these tests.
type fakeSourceDispenser struct {
	source connectorPlugin.SourcePlugin
}

func (d fakeSourceDispenser) DispenseSpecifier() (connectorPlugin.SpecifierPlugin, error) {
	return nil, cerrors.New("fakeSourceDispenser: DispenseSpecifier not implemented")
}

func (d fakeSourceDispenser) DispenseSource() (connectorPlugin.SourcePlugin, error) {
	return d.source, nil
}

func (d fakeSourceDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	return nil, cerrors.New("fakeSourceDispenser: DispenseDestination not implemented")
}

// fakePluginFetcher fulfills connector.PluginDispenserFetcher, mirroring the
// identically named test helper in pkg/connector (unexported there, so it
// can't be reused directly from this package).
type fakePluginFetcher map[string]connectorPlugin.Dispenser

func (f fakePluginFetcher) NewDispenser(_ log.CtxLogger, name string, _ string) (connectorPlugin.Dispenser, error) {
	d, ok := f[name]
	if !ok {
		return nil, cerrors.Errorf("fakePluginFetcher: no dispenser registered for plugin %q", name)
	}
	return d, nil
}

// newV1FIFOTestSource builds a real *connector.Source backed by the real
// conduit-connector-protocol v1 gRPC client, connected over an in-memory
// bufconn listener to fakeV1SourceServer. The source is already Open when this
// function returns (Configure, LifecycleOnCreated and Start have all gone over
// the real gRPC stream), so a caller can immediately call src.Ack.
func newV1FIFOTestSource(t *testing.T) (*connector.Source, *fakeV1SourceServer) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	fakeServer := &fakeV1SourceServer{}
	connectorv1.RegisterSourcePluginServer(grpcServer, fakeServer)

	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	is.NoErr(err)
	t.Cleanup(func() { _ = conn.Close() })

	// This is the real, unmodified conduit-connector-protocol v1 client -
	// the exact library standalone connectors (like the Debezium/Kafka
	// Connect wrapper) use today. Its Send implementation is what fans a
	// multi-position pconnector.SourceRunRequest out into individually
	// ordered gRPC messages (pconnector/v1/client/source.go:145-156).
	pluginClient := pconnectorv1client.NewSourcePluginClient(conn)

	logger := log.Nop()
	db := &inmemory.DB{}
	persister := connector.NewPersister(logger, db, connector.DefaultPersisterDelayThreshold, connector.DefaultPersisterBundleCountThreshold)

	instance := &connector.Instance{
		ID:   "fifo-ack-test-source",
		Type: connector.TypeSource,
		Config: connector.Config{
			Name:     "fifo-ack-test",
			Settings: map[string]string{"foo": "bar"},
		},
		PipelineID:    "fifo-ack-test-pipeline",
		Plugin:        "fifo-ack-test-plugin",
		ProvisionedBy: connector.ProvisionTypeAPI,
	}
	instance.Init(logger, persister)

	fetcher := fakePluginFetcher{instance.Plugin: fakeSourceDispenser{source: pluginClient}}
	c, err := instance.Connector(ctx, fetcher)
	is.NoErr(err)
	src, ok := c.(*connector.Source)
	is.True(ok)

	is.NoErr(src.Open(ctx))
	t.Cleanup(func() { _ = src.Teardown(ctx) })

	return src, fakeServer
}

// waitForAcks polls until fakeServer has observed at least want ack messages,
// or fails the test after a generous timeout. A gRPC Send call returning
// successfully only guarantees the message was handed to the stream, not that
// the server's Recv loop has already processed it - so a short poll (rather
// than a fixed sleep or an immediate assertion) is what makes this
// deterministic without being flaky.
func waitForAcks(t *testing.T, fakeServer *fakeV1SourceServer, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(fakeServer.observedAcks()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d acks, got %d", want, len(fakeServer.observedAcks()))
}

func assertPositionsInOrder(is *is.I, got, want []opencdc.Position) {
	is.Equal(len(got), len(want))
	for i := range want {
		is.Equal(got[i], want[i])
	}
}

// newFIFOTestWorker builds a real *Worker (via NewWorker, so timer/logger
// wiring matches production) around src, with a single no-op downstream mock
// task to satisfy the pipeline-shape validation NewWorker performs. Worker.Ack
// and Worker.Nack never invoke that downstream task's Do, so it only needs an
// ID.
func newFIFOTestWorker(t *testing.T, ctrl *gomock.Controller, logger log.CtxLogger, src *connector.Source, dlq *DLQ) *Worker {
	t.Helper()
	is := is.New(t)

	sourceTask := NewSourceTask("fifo-ack-test-source-task", src, logger, NoOpConnectorMetrics{})
	nextTask := NewMockTask(ctrl)
	nextTask.EXPECT().ID().Return("fifo-ack-test-next-task").AnyTimes()

	firstNode := &TaskNode{Task: sourceTask}
	firstNode.Next = []*TaskNode{{Task: nextTask}}

	worker, err := NewWorker(firstNode, dlq, logger, noop.Timer{})
	is.NoErr(err)
	return worker
}

// TestWorker_Ack_DeliversPositionsFIFOToV1Plugin covers the full-batch ack
// path at funnel/worker.go:493 (w.Source.Ack(ctx, originalBatch.positions)).
// It asserts a v1-protocol plugin observes every position in the batch as a
// separate message, in the exact order they appear in the batch.
func TestWorker_Ack_DeliversPositionsFIFOToV1Plugin(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	src, fakeServer := newV1FIFOTestSource(t)
	dlq, _ := NewMockDLQ(ctrl, logger)
	worker := newFIFOTestWorker(t, ctrl, logger, src, dlq)

	// Mirrors funnel/worker.go:493's real call shape: multiple positions
	// acked together in one Worker.Ack call.
	batch := randomBatch(5)

	err := worker.Ack(ctx, batch)
	is.NoErr(err)

	waitForAcks(t, fakeServer, len(batch.positions))
	assertPositionsInOrder(is, fakeServer.observedAcks(), batch.positions)
}

// TestWorker_Nack_DeliversPartialAckFIFOToV1Plugin covers the partial-batch
// ack path at funnel/worker.go:513 (w.Source.Ack(ctx,
// originalBatch.positions[:n])), which fires when a Nack's DLQ write only
// partially succeeds: the successfully DLQ'd prefix must still be acked in
// the source, in order, even though the overall Nack call returns an error.
func TestWorker_Nack_DeliversPartialAckFIFOToV1Plugin(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	src, fakeServer := newV1FIFOTestSource(t)
	// windowSize=0 disables the DLQ nack-threshold window, so every nacked
	// record in the batch is routed to the DLQ (see dlq.go's dlqWindow.store
	// short-circuit for len(window)==0), matching the existing
	// TestWorker_Nack pattern in worker_test.go.
	dlq, dlqDestinationMock := NewMockDLQ(ctrl, logger)
	worker := newFIFOTestWorker(t, ctrl, logger, src, dlq)

	batch := randomBatch(5)
	nackErr := cerrors.New("record error")
	batch.Nack(0, nackErr, nackErr, nackErr, nackErr, nackErr) // nack all 5 records

	// Simulate the DLQ destination accepting the write, but only acking
	// records 0-2: this is what makes DLQ.Nack's returned prefix count n=3
	// (out of 5), which drives the partial Source.Ack(positions[:3]) call at
	// funnel/worker.go:513 - as opposed to the previous test's full-batch call.
	dlqWriteErr := cerrors.New("dlq write failed")
	dlqDestinationMock.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	dlqDestinationMock.EXPECT().Ack(gomock.Any()).Return(
		toDestinationAcks(batch.records, []error{nil, nil, nil, dlqWriteErr, dlqWriteErr}),
		nil,
	)

	err := worker.Nack(ctx, batch, "taskID")
	is.True(err != nil) // the partial DLQ failure surfaces as a (fatal) error...

	wantPositions := batch.positions[:3] // ...but the successfully-DLQ'd prefix must still be acked, in order
	waitForAcks(t, fakeServer, len(wantPositions))
	assertPositionsInOrder(is, fakeServer.observedAcks(), wantPositions)

	// And, just as importantly: the two records that were never successfully
	// DLQ'd must NOT have been acked in the source (that would be acking a
	// record that was neither delivered downstream nor durably DLQ'd - a
	// violation of invariant 1).
	is.Equal(len(fakeServer.observedAcks()), 3)
}
