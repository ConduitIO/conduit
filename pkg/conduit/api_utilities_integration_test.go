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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit-connector-protocol/pconnutils/v1/client"
	connutilsv1 "github.com/conduitio/conduit-connector-protocol/proto/connutils/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// The connector utilities gRPC server is unauthenticated (it serves schema
// lookups to plugins Conduit starts on the same host), so it must only be
// reachable over loopback, whatever the API addresses are.
func TestConnectorUtilities_BindsLoopbackOnly(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, nil)

	addr := waitForAddress(t, a.logs, "connector utilities started")
	host, port, err := net.SplitHostPort(addr)
	is.NoErr(err)
	is.True(net.ParseIP(host).IsLoopback())

	// Reachable on loopback ...
	check := func(target string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(false))
		return err
	}
	is.NoErr(check(net.JoinHostPort("127.0.0.1", port)))

	// ... and not on any of this host's other addresses.
	addrs, err := net.InterfaceAddrs()
	is.NoErr(err)
	tried := 0
	for _, ifa := range addrs {
		ipn, ok := ifa.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		tried++
		if err := check(net.JoinHostPort(ipn.IP.String(), port)); err == nil {
			t.Errorf("connector utilities answered on non-loopback address %s", ipn.IP)
		}
	}
	if tried == 0 {
		t.Log("no non-loopback interface address on this host; only the bind address was verified")
	}
}

func dialUtilities(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Any other local process can reach the utilities port, so every call needs a
// connector token Conduit issued to a plugin it started. A real standalone
// plugin process (built on the SDK, started by the Runtime) must keep working.
func TestConnectorUtilities_RequireAConnectorToken(t *testing.T) {
	is := is.New(t)

	// Build a real plugin: it calls the schema service when its source opens.
	dir := t.TempDir()
	connectors := filepath.Join(dir, "connectors")
	is.NoErr(os.MkdirAll(connectors, 0o755))
	build := exec.CommandContext(context.Background(), "go", "build", "-o", filepath.Join(connectors, "conduit-connector-schemaplugin"),
		"../plugin/connector/standalone/test/schemaplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the test plugin: %v\n%s", err, out)
	}

	pipelines := filepath.Join(dir, "pipelines")
	is.NoErr(os.MkdirAll(pipelines, 0o755))
	result := filepath.Join(dir, "result.txt")
	is.NoErr(os.WriteFile(filepath.Join(pipelines, "p.yaml"), []byte(`version: "2.2"
pipelines:
  - id: schema-plugin-pipeline
    status: running
    name: schema-plugin-pipeline
    dead-letter-queue:
      plugin: builtin:log
      settings:
        payload: "true"
    connectors:
      - id: src
        type: source
        plugin: standalone:schemaplugin
        settings:
          resultFile: `+result+`
      - id: dst
        type: destination
        plugin: builtin:log
`), 0o600))

	a := startSecureAPI(t, func(c *Config) {
		c.Connectors.Path = connectors
		c.Pipelines.Path = pipelines
	})
	utilAddr := waitForAddress(t, a.logs, "connector utilities started")

	// The plugin Conduit spawned reached the schema service with its token.
	deadline := time.Now().Add(30 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(result); err == nil && len(b) > 0 {
			got = b
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if string(got) != "ok" {
		t.Logf("runtime logs:\n%s", a.logs.String())
	}
	is.Equal(string(got), "ok")

	// A local process without a token gets nothing.
	conn := dialUtilities(t, utilAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.NewSchemaServiceClient(conn).CreateSchema(ctx, pconnutils.CreateSchemaRequest{Subject: "s", Type: schema.TypeAvro, Bytes: []byte(`"string"`)})
	is.True(err != nil)
	_, err = client.NewSchemaServiceClient(conn).GetSchema(ctx, pconnutils.GetSchemaRequest{Subject: "s", Version: 1})
	is.True(err != nil)
	_, err = connutilsv1.NewSchemaServiceClient(conn).CreateSchema(ctx, &connutilsv1.CreateSchemaRequest{Subject: "s"})
	is.Equal(status.Code(err), codes.Unauthenticated)

	// With a made-up token too.
	_, err = client.NewSchemaServiceClient(conn).CreateSchema(
		pconnutils.ContextWithConnectorToken(ctx, `{"value":"`+strings.Repeat("a", 64)+`","connector_id":"x"}`),
		pconnutils.CreateSchemaRequest{Subject: "s", Type: schema.TypeAvro, Bytes: []byte(`"string"`)})
	is.True(err != nil)

	// Reflection is not offered, and is not reachable without a token.
	stream, err := grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	is.NoErr(err)
	_ = stream.Send(&grpc_reflection_v1.ServerReflectionRequest{MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{}})
	_, err = stream.Recv()
	is.True(err != nil)

	// The refusals are counted with the API ones.
	is.True(strings.Contains(a.logs.String(), "rejected connector utilities request"))
	is.True(!strings.Contains(a.logs.String(), strings.Repeat("a", 64)))
}
