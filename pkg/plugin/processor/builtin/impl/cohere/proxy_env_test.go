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

package cohere

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// proxyListener counts connections made to it and drops them.
func proxyListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return "http://" + l.Addr().String(), &n
}

// The SDK clients used to inherit http.DefaultTransport and so HTTP(S)_PROXY:
// a proxy the dial guard never validated (GHSA-m3mw-fpw8-w765). With the
// guarded client the request is dialed directly and the proxy sees nothing,
// whether or not the network is reachable from where the test runs.
func TestEmbed_IgnoresProxyEnvironment(t *testing.T) {
	proxy, hits := proxyListener(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY"} {
		t.Setenv(k, proxy)
	}
	t.Setenv("NO_PROXY", "")

	ctx := context.Background()
	p := NewEmbedProcessor(log.Nop())
	if err := p.Configure(ctx, config.Config{"apiKey": "k", "model": "embed-english-v2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = p.Process(cctx, []opencdc.Record{{Payload: opencdc.Change{After: opencdc.RawData("hello")}}})

	if hits.Load() != 0 {
		t.Fatalf("request went through the environment proxy (%d connections)", hits.Load())
	}
}
