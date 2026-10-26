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

package api

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/http/apisec"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

const clientTestToken = "0123456789abcdef0123456789abcdef"

// startGatedServer serves the gRPC health service behind the token gate. The
// health Check is exempt from the gate, so the gate is exercised through
// Watch, which the client does not call; the tests therefore assert on what
// the client sends using a recording interceptor.
func startGatedServer(t *testing.T) (addr string, seen chan string) {
	t.Helper()
	seen = make(chan string, 8)
	auth := apisec.NewAuthenticator(clientTestToken, apisec.NewRejections(nil, log.Nop()))
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				seen <- authorizationOf(ctx)
				return h(ctx, req)
			},
			auth.UnaryInterceptor(),
		),
	)
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, hs)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String(), seen
}

func recv(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached the server")
		return ""
	}
}

func TestNewClient_SendsTokenFromOption(t *testing.T) {
	is := is.New(t)
	addr, seen := startGatedServer(t)
	c, err := NewClient(context.Background(), addr, WithTokenSource(clientTestToken, ""))
	is.NoErr(err)
	defer c.Close()
	is.Equal(recv(t, seen), "Bearer "+clientTestToken)
}

func TestNewClient_SendsTokenFromFileOption(t *testing.T) {
	is := is.New(t)
	f := filepath.Join(t.TempDir(), "token")
	is.NoErr(os.WriteFile(f, []byte(clientTestToken+"\n"), 0o600))
	addr, seen := startGatedServer(t)
	c, err := NewClient(context.Background(), addr, WithTokenSource("", f))
	is.NoErr(err)
	defer c.Close()
	is.Equal(recv(t, seen), "Bearer "+clientTestToken)
}

func TestNewClient_FallsBackToEnvironment(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		is := is.New(t)
		t.Setenv(EnvToken, clientTestToken)
		t.Setenv(EnvTokenFile, "")
		addr, seen := startGatedServer(t)
		c, err := NewClient(context.Background(), addr)
		is.NoErr(err)
		defer c.Close()
		is.Equal(recv(t, seen), "Bearer "+clientTestToken)
	})
	t.Run("token file", func(t *testing.T) {
		is := is.New(t)
		f := filepath.Join(t.TempDir(), "token")
		is.NoErr(os.WriteFile(f, []byte(clientTestToken), 0o600))
		t.Setenv(EnvToken, "")
		t.Setenv(EnvTokenFile, f)
		addr, seen := startGatedServer(t)
		c, err := NewClient(context.Background(), addr)
		is.NoErr(err)
		defer c.Close()
		is.Equal(recv(t, seen), "Bearer "+clientTestToken)
	})
	t.Run("explicit option wins over environment", func(t *testing.T) {
		is := is.New(t)
		t.Setenv(EnvToken, "environment-token-environment-token")
		addr, seen := startGatedServer(t)
		c, err := NewClient(context.Background(), addr, WithTokenSource(clientTestToken, ""))
		is.NoErr(err)
		defer c.Close()
		is.Equal(recv(t, seen), "Bearer "+clientTestToken)
	})
}

func TestNewClient_NoTokenSendsNone(t *testing.T) {
	is := is.New(t)
	t.Setenv(EnvToken, "")
	t.Setenv(EnvTokenFile, "")
	addr, seen := startGatedServer(t)
	c, err := NewClient(context.Background(), addr)
	is.NoErr(err)
	defer c.Close()
	is.Equal(recv(t, seen), "")
}

func TestNewClient_UnreadableTokenFileIsCodedError(t *testing.T) {
	is := is.New(t)
	_, err := NewClient(context.Background(), "127.0.0.1:1", WithTokenSource("", filepath.Join(t.TempDir(), "missing")))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, apisec.CodeTokenInvalid)
	is.Equal(ce.ConfigPath, apisec.ConfigPathTokenFile)
}

func authorizationOf(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) > 0 {
		return v[0]
	}
	return ""
}
