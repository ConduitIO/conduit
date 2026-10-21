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
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// secAPI is a running Runtime with both APIs on free loopback ports. The
// security integration tests (token, Origin, Host, Content-Type, WebSocket)
// drive it over real sockets, exactly as a browser, script or CLI would.
type secAPI struct {
	r        *Runtime
	httpAddr string // host:port
	grpcAddr string // host:port
}

// startSecureAPI starts a Runtime whose API config is DefaultConfig's, with
// both addresses forced to 127.0.0.1:0, then lets mutate adjust the config.
func startSecureAPI(t *testing.T, mutate func(*Config)) *secAPI {
	t.Helper()
	is := is.New(t)

	logs := &lockedBuffer{}
	cfg := DefaultConfigWithBasePath(t.TempDir())
	cfg.DB.Type = DBTypeInMemory
	cfg.Pipelines.Path = t.TempDir()
	cfg.API.GRPC.Address = "127.0.0.1:0"
	cfg.API.HTTP.Address = "127.0.0.1:0"
	cfg.Log.NewLogger = func(string, string) log.CtxLogger {
		return log.New(zerolog.New(logs).Level(zerolog.InfoLevel))
	}
	if mutate != nil {
		mutate(&cfg)
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

	return &secAPI{
		r:        r,
		grpcAddr: waitForAddress(t, logs, "grpc API started"),
		httpAddr: waitForAddress(t, logs, "http API started"),
	}
}

// port returns the HTTP API's port.
func (a *secAPI) port(t *testing.T) string {
	t.Helper()
	_, p, err := net.SplitHostPort(a.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// pipelineCount is the number of pipelines in the engine, read in-process so
// it is independent of the API's own access controls.
func (a *secAPI) pipelineCount() int {
	return len(a.r.Orchestrator.Pipelines.List(context.Background()))
}

// do sends one HTTP request to the API. headers with an empty value are
// omitted; a "Host" key overrides the request's Host header.
func (a *secAPI) do(t *testing.T, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+a.httpAddr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		switch {
		case v == "":
		case strings.EqualFold(k, "Host"):
			req.Host = v
		default:
			req.Header.Set(k, v)
		}
	}
	// No redirects, no cookie jar: what the server answered is what we assert.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func mkdir(path string) error { return os.MkdirAll(path, 0o755) }
