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

package mcp

import (
	"context"
	"testing"

	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTestClient wires srv to a fresh *sdkmcp.ClientSession over an
// in-memory transport pair, per go-sdk's NewInMemoryTransports doc (servers
// must connect before clients). Both ends are closed via t.Cleanup.
func connectTestClient(t *testing.T, srv *sdkmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	is.NoErr(err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	is.NoErr(err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

// listToolNames returns the sorted-by-catalog-order tool names srv currently
// advertises via tools/list.
func listToolNames(t *testing.T, cs *sdkmcp.ClientSession) []string {
	t.Helper()
	is := is.New(t)

	res, err := cs.ListTools(context.Background(), nil)
	is.NoErr(err)

	names := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		names[i] = tool.Name
	}
	return names
}

const validPipelineYAML = `version: 2.2
pipelines:
  - id: orders
    name: orders
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
`
