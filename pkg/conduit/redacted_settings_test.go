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

package conduit

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/orchestrator"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
)

// These tests are the #2913 regression tests. Every API response redacts
// Settings values to log.Redacted ("***"). A client that reads an entity and
// sends its settings back must not overwrite the stored values with "***".
// They run a real Runtime and go through both the gRPC server and the HTTP
// gateway, which is what a UI or script would use.

// lockedBuffer is a bytes.Buffer safe for the runtime's logger and the test to
// share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type apiHarness struct {
	r        *Runtime
	grpcConn *grpc.ClientConn
	httpBase string

	pipelines  apiv1.PipelineServiceClient
	connectors apiv1.ConnectorServiceClient
	processors apiv1.ProcessorServiceClient
}

// startAPI runs a Runtime with an in-memory DB and both APIs on free ports,
// and returns clients for them once both have started.
func startAPI(t *testing.T) *apiHarness {
	t.Helper()
	is := is.New(t)

	logs := &lockedBuffer{}
	cfg := DefaultConfigWithBasePath(t.TempDir())
	cfg.DB.Type = DBTypeInMemory
	cfg.Pipelines.Path = t.TempDir() // empty: API-created entities only
	cfg.API.GRPC.Address = "127.0.0.1:0"
	cfg.API.HTTP.Address = "127.0.0.1:0"
	cfg.Log.NewLogger = func(string, string) log.CtxLogger {
		return log.New(zerolog.New(logs).Level(zerolog.InfoLevel))
	}

	r, err := NewRuntime(cfg)
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("runtime did not stop")
		}
	})

	grpcAddr := waitForAddress(t, logs, "grpc API started")
	httpAddr := waitForAddress(t, logs, "http API started")

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	is.NoErr(err)
	t.Cleanup(func() { _ = conn.Close() })

	return &apiHarness{
		r:          r,
		grpcConn:   conn,
		httpBase:   "http://" + httpAddr,
		pipelines:  apiv1.NewPipelineServiceClient(conn),
		connectors: apiv1.NewConnectorServiceClient(conn),
		processors: apiv1.NewProcessorServiceClient(conn),
	}
}

// waitForAddress returns the address logged with msg.
func waitForAddress(t *testing.T, logs *lockedBuffer, msg string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) != nil || entry["message"] != msg {
				continue
			}
			addr, _ := entry[log.ServerAddressField].(string)
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatalf("bad address %q in %q log line", addr, msg)
			}
			return net.JoinHostPort("127.0.0.1", port)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q was not logged in time; logs:\n%s", msg, logs.String())
	return ""
}

// httpJSON sends body as JSON and returns the status code and response body.
func (h *apiHarness) httpJSON(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.httpBase+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(out)
}

// fixture is one pipeline with a destination connector and a processor, all
// created through the API (so they are API-provisioned and updatable).
type fixture struct {
	pipelineID  string
	connectorID string
	processorID string
}

var (
	storedConnectorSettings = map[string]string{"level": "info", "message": "connector-secret"}
	storedProcessorSettings = map[string]string{"field": ".Metadata.key", "value": "processor-secret"}
	storedDLQSettings       = map[string]string{"level": "warn", "message": "dlq-secret"}
)

func (h *apiHarness) newFixture(t *testing.T) fixture {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	pl, err := h.pipelines.CreatePipeline(ctx, &apiv1.CreatePipelineRequest{
		Config: &apiv1.Pipeline_Config{Name: "redacted-" + strings.ReplaceAll(t.Name(), "/", "-")},
	})
	is.NoErr(err)

	conn, err := h.connectors.CreateConnector(ctx, &apiv1.CreateConnectorRequest{
		Type:       apiv1.Connector_TYPE_DESTINATION,
		Plugin:     "builtin:log",
		PipelineId: pl.Pipeline.Id,
		Config:     &apiv1.Connector_Config{Name: "dest", Settings: storedConnectorSettings},
	})
	is.NoErr(err)

	proc, err := h.processors.CreateProcessor(ctx, &apiv1.CreateProcessorRequest{
		Plugin: "field.set",
		Parent: &apiv1.Processor_Parent{Type: apiv1.Processor_Parent_TYPE_PIPELINE, Id: pl.Pipeline.Id},
		Config: &apiv1.Processor_Config{Settings: storedProcessorSettings, Workers: 1},
	})
	is.NoErr(err)

	_, err = h.pipelines.UpdateDLQ(ctx, &apiv1.UpdateDLQRequest{
		Id:  pl.Pipeline.Id,
		Dlq: &apiv1.Pipeline_DLQ{Plugin: "builtin:log", Settings: storedDLQSettings, WindowSize: 1},
	})
	is.NoErr(err)

	return fixture{pipelineID: pl.Pipeline.Id, connectorID: conn.Connector.Id, processorID: proc.Processor.Id}
}

// stored reads the real, unredacted settings straight from the services.
func (h *apiHarness) stored(t *testing.T, f fixture) (conn, proc, dlq map[string]string) {
	t.Helper()
	ctx := context.Background()
	c, err := h.r.connectorService.Get(ctx, f.connectorID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.r.processorService.Get(ctx, f.processorID)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := h.r.pipelineService.Get(ctx, f.pipelineID)
	if err != nil {
		t.Fatal(err)
	}
	return c.Config.Settings, p.Config.Settings, pl.DLQ.Settings
}

func redactedCopy(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k := range in {
		out[k] = log.Redacted
	}
	return out
}

// TestRedactedRoundTrip_GRPC reads each entity over gRPC, sends exactly what
// it got back, and checks the stored secrets survived.
func TestRedactedRoundTrip_GRPC(t *testing.T) {
	is := is.New(t)
	h := startAPI(t)
	f := h.newFixture(t)
	ctx := context.Background()

	gotConn, err := h.connectors.GetConnector(ctx, &apiv1.GetConnectorRequest{Id: f.connectorID})
	is.NoErr(err)
	is.Equal(gotConn.Connector.Config.Settings, redactedCopy(storedConnectorSettings)) // the API redacts
	_, err = h.connectors.UpdateConnector(ctx, &apiv1.UpdateConnectorRequest{
		Id: f.connectorID, Plugin: gotConn.Connector.Plugin, Config: gotConn.Connector.Config,
	})
	is.NoErr(err)

	gotProc, err := h.processors.GetProcessor(ctx, &apiv1.GetProcessorRequest{Id: f.processorID})
	is.NoErr(err)
	_, err = h.processors.UpdateProcessor(ctx, &apiv1.UpdateProcessorRequest{
		Id: f.processorID, Plugin: gotProc.Processor.Plugin, Config: gotProc.Processor.Config,
	})
	is.NoErr(err)

	gotDLQ, err := h.pipelines.GetDLQ(ctx, &apiv1.GetDLQRequest{Id: f.pipelineID})
	is.NoErr(err)
	_, err = h.pipelines.UpdateDLQ(ctx, &apiv1.UpdateDLQRequest{Id: f.pipelineID, Dlq: gotDLQ.Dlq})
	is.NoErr(err)

	conn, proc, dlq := h.stored(t, f)
	is.Equal(conn, storedConnectorSettings)
	is.Equal(proc, storedProcessorSettings)
	is.Equal(dlq, storedDLQSettings)
}

// TestRedactedSettings_HTTPGateway covers the same semantics through the HTTP
// gateway: a round trip keeps stored values, a real value replaces one, a
// key left out is removed (an update replaces the whole map, as before), and
// "***" for a key with no stored value is refused without changing anything.
func TestRedactedSettings_HTTPGateway(t *testing.T) {
	h := startAPI(t)

	type target struct {
		name   string
		path   func(f fixture) string
		body   func(settings map[string]string) any
		stored func(conn, proc, dlq map[string]string) map[string]string
		orig   map[string]string
		// changeKey is a key whose value the "new value" case replaces.
		changeKey string
	}
	targets := []target{
		{
			name: "connector",
			path: func(f fixture) string { return "/v1/connectors/" + f.connectorID },
			body: func(s map[string]string) any {
				return map[string]any{"plugin": "builtin:log", "config": map[string]any{"name": "dest", "settings": s}}
			},
			stored:    func(c, _, _ map[string]string) map[string]string { return c },
			orig:      storedConnectorSettings,
			changeKey: "message",
		},
		{
			name: "processor",
			path: func(f fixture) string { return "/v1/processors/" + f.processorID },
			body: func(s map[string]string) any {
				return map[string]any{"plugin": "field.set", "config": map[string]any{"settings": s, "workers": 1}}
			},
			stored:    func(_, p, _ map[string]string) map[string]string { return p },
			orig:      storedProcessorSettings,
			changeKey: "value",
		},
		{
			name: "dlq",
			path: func(f fixture) string { return "/v1/pipelines/" + f.pipelineID + "/dead-letter-queue" },
			body: func(s map[string]string) any {
				return map[string]any{"plugin": "builtin:log", "settings": s, "windowSize": 1}
			},
			stored:    func(_, _, d map[string]string) map[string]string { return d },
			orig:      storedDLQSettings,
			changeKey: "message",
		},
	}

	for _, tg := range targets {
		t.Run(tg.name, func(t *testing.T) {
			t.Run("round trip keeps the stored values", func(t *testing.T) {
				is := is.New(t)
				f := h.newFixture(t)

				code, body := h.httpJSON(t, http.MethodPut, tg.path(f), tg.body(redactedCopy(tg.orig)))
				is.Equal(code, http.StatusOK)
				is.True(!strings.Contains(body, "secret")) // the response is redacted too
				is.Equal(tg.stored(h.stored(t, f)), tg.orig)
			})

			t.Run("a real value replaces the stored one", func(t *testing.T) {
				is := is.New(t)
				f := h.newFixture(t)

				update := redactedCopy(tg.orig)
				update[tg.changeKey] = "new-value"
				code, _ := h.httpJSON(t, http.MethodPut, tg.path(f), tg.body(update))
				is.Equal(code, http.StatusOK)

				want := map[string]string{}
				for k, v := range tg.orig {
					want[k] = v
				}
				want[tg.changeKey] = "new-value"
				is.Equal(tg.stored(h.stored(t, f)), want)
			})

			t.Run("a value that only contains *** is a real value", func(t *testing.T) {
				is := is.New(t)
				f := h.newFixture(t)

				update := redactedCopy(tg.orig)
				update[tg.changeKey] = "a***b"
				code, _ := h.httpJSON(t, http.MethodPut, tg.path(f), tg.body(update))
				is.Equal(code, http.StatusOK)
				is.Equal(tg.stored(h.stored(t, f))[tg.changeKey], "a***b")
			})

			t.Run("a key left out is removed, as before", func(t *testing.T) {
				is := is.New(t)
				f := h.newFixture(t)

				update := redactedCopy(tg.orig)
				delete(update, tg.changeKey)
				code, _ := h.httpJSON(t, http.MethodPut, tg.path(f), tg.body(update))
				is.Equal(code, http.StatusOK)

				want := map[string]string{}
				for k, v := range tg.orig {
					if k != tg.changeKey {
						want[k] = v
					}
				}
				is.Equal(tg.stored(h.stored(t, f)), want)
			})

			t.Run("*** for a key with no stored value is refused", func(t *testing.T) {
				is := is.New(t)
				f := h.newFixture(t)

				update := redactedCopy(tg.orig)
				update["not.stored"] = log.Redacted
				code, body := h.httpJSON(t, http.MethodPut, tg.path(f), tg.body(update))
				is.Equal(code, http.StatusBadRequest)
				is.True(strings.Contains(body, orchestrator.CodeRedactedSettingWithoutStoredValue.Reason()))
				is.True(strings.Contains(body, "not.stored"))
				is.Equal(tg.stored(h.stored(t, f)), tg.orig) // nothing changed
			})
		})
	}
}

// The refusal reaches a gRPC client as InvalidArgument with the stable reason.
func TestRedactedSettingWithoutStoredValue_GRPCStatus(t *testing.T) {
	is := is.New(t)
	h := startAPI(t)
	f := h.newFixture(t)

	settings := redactedCopy(storedConnectorSettings)
	settings["not.stored"] = log.Redacted
	_, err := h.connectors.UpdateConnector(context.Background(), &apiv1.UpdateConnectorRequest{
		Id: f.connectorID, Plugin: "builtin:log", Config: &apiv1.Connector_Config{Name: "dest", Settings: settings},
	})
	st, ok := grpcstatus.FromError(err)
	is.True(ok)
	is.Equal(st.Code(), codes.InvalidArgument)
	is.True(strings.Contains(st.Message(), `"not.stored"`))
}
