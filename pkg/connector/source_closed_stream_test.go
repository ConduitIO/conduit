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

package connector

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/v2/client"
	"github.com/conduitio/conduit-connector-protocol/pconnector/v2/server"
	connectorv2 "github.com/conduitio/conduit-connector-protocol/proto/connector/v2"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// promptTeardown bounds how long Teardown may take once the plugin has closed
// its run stream. It is far below both the teardown budget
// (DefaultTeardownFlushTimeout, 10s) and the time one deferred ack's default
// retry schedule takes (~3s), so the tests below fail on code that retries a
// closed stream, and is loose enough not to flake under -race.
const promptTeardown = time.Second

// recordingSourceStream records the outcome of every Send so a test can assert
// what the delivery goroutine actually got back from the transport.
type recordingSourceStream struct {
	pconnector.SourceRunStreamClient

	mu        sync.Mutex
	delivered int
	sendErrs  []error
}

func (r *recordingSourceStream) Send(req pconnector.SourceRunRequest) error {
	err := r.SourceRunStreamClient.Send(req)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.delivered++
	} else {
		r.sendErrs = append(r.sendErrs, err)
	}
	return err
}

func (r *recordingSourceStream) result() (delivered int, sendErrs []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered, append([]error(nil), r.sendErrs...)
}

// TestSource_Teardown_PluginClosedStream_ReturnsPromptly is the regression test
// for #2900. When the plugin ends its run stream (its Run returned an error),
// a deferred ack can no longer be delivered: Send on that stream returns
// io.EOF and keeps returning it. Teardown used to spend its whole budget
// retrying those sends with backoff (~3s per queued ack, capped by the 10s
// budget), delaying every source failure and every shutdown after one.
//
// The acks must stay undelivered (never reported as sent) and Teardown must
// still return cleanly: the positions are durable, so the plugin resumes from
// them and replays whatever its upstream had not committed (invariant 3).
func TestSource_Teardown_PluginClosedStream_ReturnsPromptly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	rec := &recordingSourceStream{SourceRunStreamClient: src.stream}
	src.stream = rec

	// The plugin's Run fails: the stream closes from the plugin side, as the
	// mock plugin and a real gRPC plugin both do.
	stream.Close(cerrors.New("plugin run failed"))
	_, err := src.Read(ctx)
	is.True(err != nil) // the node learns about the failure from Read

	// Records read before the failure are still acked by the pipeline. Their
	// positions become durable and their acks are queued for delivery.
	for _, p := range []string{"pos-1", "pos-2", "pos-3"} {
		is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position(p)}))
	}

	start := time.Now()
	is.NoErr(src.Teardown(ctx))
	took := time.Since(start)
	if took > promptTeardown {
		t.Fatalf("Teardown took %v after the plugin closed its stream; want < %v (it retried acks to a closed stream)", took, promptTeardown)
	}

	delivered, sendErrs := rec.result()
	is.Equal(delivered, 0) // nothing was reported delivered on a closed stream
	for _, err := range sendErrs {
		is.True(cerrors.Is(err, io.EOF)) // the closed stream only ever answered io.EOF
	}
}

// TestSource_DeferredAck_PluginClosedStream_NotRetriedNotEscalated checks the
// running (not tearing down) side of #2900: a deferred ack that hits a stream
// the plugin closed is sent once and not retried, and nothing is escalated on
// errs. The node learns why the stream ended from Read, which returns the
// plugin's own error; an io.EOF escalated on errs could reach the node first
// and replace a fatal cause with a retryable one (#1659).
func TestSource_DeferredAck_PluginClosedStream_NotRetriedNotEscalated(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	rec := &recordingSourceStream{SourceRunStreamClient: src.stream}
	src.stream = rec
	pluginErr := cerrors.New("plugin run failed")
	stream.Close(pluginErr)

	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("pos-1")}))

	// The first send happens once the flush lands; wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, errs := rec.result(); len(errs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deferred ack was never attempted")
		}
		time.Sleep(time.Millisecond)
	}
	// With the default schedule, a retry would follow within 10ms, and three
	// more within 150ms. Neither a retry nor an escalation may happen.
	select {
	case err := <-src.Errors():
		t.Fatalf("closed stream was escalated on errs: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	delivered, sendErrs := rec.result()
	is.Equal(delivered, 0)
	is.Equal(len(sendErrs), 1) // sent once, not retried
	is.True(cerrors.Is(sendErrs[0], io.EOF))

	_, err := src.Read(ctx)
	is.True(cerrors.Is(err, pluginErr)) // Read reports the plugin's real error

	start := time.Now()
	is.NoErr(src.Teardown(ctx))
	if took := time.Since(start); took > promptTeardown {
		t.Fatalf("Teardown took %v; want < %v", took, promptTeardown)
	}
}

// TestSource_Teardown_AbortsBlockedEscalation covers the other way #2900's
// delay showed up: the delivery goroutine exhausted its retries while the
// plugin was running and blocked sending the escalation on errs, and then the
// node stopped reading errs and called Teardown. The escalation only gave up
// when streamCtx was canceled, which Teardown does after the drain wait, so
// the drain always ran out its whole budget.
func TestSource_Teardown_AbortsBlockedEscalation(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	_ = expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	faulty := &faultySourceStream{
		SourceRunStreamClient: src.stream,
		failsLeft:             1 << 30,
		failErr:               cerrors.New("permanent send failure"),
	}
	src.stream = faulty
	src.deferredAckMaxRetries = 2
	src.deferredAckBackoffCap = time.Millisecond

	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("pos-1")}))

	// Wait until the retries are exhausted; the goroutine then goes straight
	// to the escalation, which nobody reads.
	deadline := time.Now().Add(5 * time.Second)
	for faulty.failures() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("deferred ack was never attempted")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	is.NoErr(src.Teardown(ctx))
	if took := time.Since(start); took > promptTeardown {
		t.Fatalf("Teardown took %v with an escalation pending; want < %v", took, promptTeardown)
	}
}

// TestSource_Teardown_PluginClosedGRPCStream_ReturnsPromptly runs #2900's
// scenario over a real gRPC stream (the connector protocol's v2 client and
// server over an in-process listener, the same code a standalone plugin
// uses). It pins the transport behavior the fix relies on: once the plugin's
// Run has returned, Send fails with io.EOF and keeps failing, so no retry can
// deliver the ack.
func TestSource_Teardown_PluginClosedGRPCStream_ReturnsPromptly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// Plugin side: a mocked plugin behind the real v2 gRPC server.
	pluginMock := mock.NewSourcePlugin(ctrl)
	runFail := make(chan struct{})
	pluginMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.SourceConfigureResponse{}, nil)
	pluginMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	pluginMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, nil)
	pluginMock.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ pconnector.SourceRunStream) error {
			select {
			case <-runFail:
				return cerrors.New("plugin run failed")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	pluginMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)

	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	connectorv2.RegisterSourcePluginServer(grpcServer, server.NewSourcePluginServer(pluginMock))
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	is.NoErr(err)
	t.Cleanup(func() { _ = conn.Close() })

	// Conduit side: the real Source, dispensed the real v2 gRPC client.
	logger := log.Nop()
	persister := NewPersister(logger, &inmemory.DB{}, DefaultPersisterDelayThreshold, 1)
	instance := &Instance{
		ID:     "test-connector-id",
		Type:   TypeSource,
		Config: Config{Name: "test-name", Settings: map[string]string{"foo": "bar"}},
		Plugin: "test-plugin",
	}
	instance.Init(logger, persister)
	dispenser := mock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(client.NewSourcePluginClient(conn), nil).AnyTimes()
	c, err := instance.Connector(ctx, fakePluginFetcher{instance.Plugin: dispenser})
	is.NoErr(err)
	src := c.(*Source)

	is.NoErr(src.Open(ctx))
	rec := &recordingSourceStream{SourceRunStreamClient: src.stream}
	src.stream = rec

	close(runFail)
	_, err = src.Read(ctx)
	is.True(err != nil) // Recv surfaces the plugin's failure

	for _, p := range []string{"pos-1", "pos-2", "pos-3"} {
		is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position(p)}))
	}

	start := time.Now()
	is.NoErr(src.Teardown(ctx))
	took := time.Since(start)
	if took > promptTeardown {
		t.Fatalf("Teardown took %v after the gRPC plugin closed its stream; want < %v", took, promptTeardown)
	}

	delivered, sendErrs := rec.result()
	is.Equal(delivered, 0)
	is.True(len(sendErrs) > 0) // the acks were attempted
	for _, err := range sendErrs {
		is.True(cerrors.Is(err, io.EOF)) // gRPC reports a stream the server ended as io.EOF on Send
	}
}
