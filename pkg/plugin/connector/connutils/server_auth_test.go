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

package connutils

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit-connector-protocol/pconnutils/v1/client"
	"github.com/conduitio/conduit-connector-protocol/pconnutils/v1/server"
	connutilsv1 "github.com/conduitio/conduit-connector-protocol/proto/connutils/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/http/apisec"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
)

func TestAuthManager_TokenIs256BitsAndUnique(t *testing.T) {
	is := is.New(t)
	am := NewAuthManager()
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		tkn := am.GenerateNew("conn")
		var parsed token
		is.NoErr(json.Unmarshal([]byte(tkn), &parsed))
		is.Equal(parsed.ConnectorID, "conn")
		is.Equal(len(parsed.Value), 64)                              // 32 bytes, hex
		is.Equal(strings.Trim(parsed.Value, "0123456789abcdef"), "") // lower-case hex only
		is.True(!seen[parsed.Value])
		seen[parsed.Value] = true
	}
}

func TestAuthManager_ErrorsNeverEchoTheToken(t *testing.T) {
	is := is.New(t)
	am := NewAuthManager()
	am.GenerateNew("conn")
	err := am.IsTokenValid("presented-secret-value")
	is.True(err != nil)
	is.True(!strings.Contains(err.Error(), "presented-secret-value"))
}

func TestAuthManager_NearMissesAreRejected(t *testing.T) {
	is := is.New(t)
	am := NewAuthManager()
	tkn := am.GenerateNew("conn")
	for _, bad := range []string{"", " ", tkn[:len(tkn)-1], tkn + " ", tkn + "x", strings.ToUpper(tkn), "x" + tkn[1:]} {
		is.True(am.IsTokenValid(bad) != nil)
	}
	is.NoErr(am.IsTokenValid(tkn))
}

func TestAuthManager_Concurrent(t *testing.T) {
	am := NewAuthManager()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tkn := am.GenerateNew("c")
			if am.IsTokenValid(tkn) != nil {
				t.Error("fresh token rejected")
			}
			am.Deregister(tkn)
			if am.IsTokenValid(tkn) == nil {
				t.Error("deregistered token accepted")
			}
		}()
	}
	wg.Wait()
}

type fakeSchemaService struct{}

func (fakeSchemaService) CreateSchema(context.Context, pconnutils.CreateSchemaRequest) (pconnutils.CreateSchemaResponse, error) {
	return pconnutils.CreateSchemaResponse{Schema: schema.Schema{Subject: "s", Version: 1, Type: schema.TypeAvro}}, nil
}

func (fakeSchemaService) GetSchema(context.Context, pconnutils.GetSchemaRequest) (pconnutils.GetSchemaResponse, error) {
	return pconnutils.GetSchemaResponse{Schema: schema.Schema{Subject: "s", Version: 1, Type: schema.TypeAvro}}, nil
}

// startGuardedServer serves the schema service, the health service and server
// reflection (to prove the interceptor covers more than the schema handlers)
// behind the connector-token interceptors, on a real loopback listener.
func startGuardedServer(t *testing.T, am *AuthManager) *grpc.ClientConn {
	t.Helper()
	rej := apisec.NewRejections(nil, log.Nop())
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(am.UnaryServerInterceptor(rej)),
		grpc.ChainStreamInterceptor(am.StreamServerInterceptor(rej)),
	)
	connutilsv1.RegisterSchemaServiceServer(srv, server.NewSchemaServiceServer(fakeSchemaService{}))
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	// A stand-in for "an RPC someone adds later that forgets to check the token".
	registerReflection(srv)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestServerAuth_SchemaCalls(t *testing.T) {
	am := NewAuthManager()
	conn := startGuardedServer(t, am)
	svc := client.NewSchemaServiceClient(conn)
	valid := am.GenerateNew("conn-1")
	revoked := am.GenerateNew("conn-2")
	am.Deregister(revoked)

	call := func(t *testing.T, tkn string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if tkn != "" {
			ctx = pconnutils.ContextWithConnectorToken(ctx, tkn)
		}
		_, err := svc.CreateSchema(ctx, pconnutils.CreateSchemaRequest{Subject: "s", Type: schema.TypeAvro, Bytes: []byte(`"string"`)})
		return err
	}

	t.Run("a spawned plugin's client with its token works", func(t *testing.T) {
		is.New(t).NoErr(call(t, valid))
	})
	for name, tkn := range map[string]string{
		"no token":      "",
		"unknown token": `{"value":"` + strings.Repeat("0", 64) + `","connector_id":"conn-1"}`,
		"revoked token": revoked,
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			is := is.New(t)
			err := call(t, tkn)
			is.True(err != nil)
			is.True(!strings.Contains(err.Error(), tkn) || tkn == "")
		})
	}
}

func TestServerAuth_RawClientWithoutOrWithWrongMetadata(t *testing.T) {
	am := NewAuthManager()
	conn := startGuardedServer(t, am)
	valid := am.GenerateNew("conn-1")
	c := connutilsv1.NewSchemaServiceClient(conn)
	req := &connutilsv1.CreateSchemaRequest{Subject: "s"}

	ctx := context.Background()
	_, err := c.CreateSchema(ctx, req)
	is.New(t).Equal(status.Code(err), codes.Unauthenticated)
	ce := conduiterr.FromStatus(status.Convert(err))
	is.New(t).Equal(ce.Code, apisec.CodeUnauthenticated)
	is.New(t).True(ce.Suggestion != "")

	// The right token under the wrong key, or as an API-style header, is not a credential.
	for _, key := range []string{"authorization", "x-conduit-token", "conduit-connector-token-x"} {
		_, err = c.CreateSchema(metadata.AppendToOutgoingContext(ctx, key, valid), req)
		is.New(t).Equal(status.Code(err), codes.Unauthenticated)
	}
	_, err = c.CreateSchema(metadata.AppendToOutgoingContext(ctx, "conduit-connector-token", valid), req)
	is.New(t).NoErr(err)
}

func TestServerAuth_CoversTheWholeServer(t *testing.T) {
	am := NewAuthManager()
	conn := startGuardedServer(t, am)
	valid := am.GenerateNew("conn-1")
	ctx := context.Background()

	t.Run("reflection without a token", func(t *testing.T) {
		is := is.New(t)
		stream, err := grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
		is.NoErr(err)
		_ = stream.Send(&grpc_reflection_v1.ServerReflectionRequest{MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{}})
		_, err = stream.Recv()
		is.Equal(status.Code(err), codes.Unauthenticated)
	})
	t.Run("reflection with a token works (the gate is the token, not the method)", func(t *testing.T) {
		is := is.New(t)
		stream, err := grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(metadata.AppendToOutgoingContext(ctx, "conduit-connector-token", valid))
		is.NoErr(err)
		is.NoErr(stream.Send(&grpc_reflection_v1.ServerReflectionRequest{MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{}}))
		_, err = stream.Recv()
		is.NoErr(err)
	})
	t.Run("an unknown method without a token never succeeds", func(t *testing.T) {
		is := is.New(t)
		// grpc-go answers an unregistered method with Unimplemented before any
		// interceptor runs; no handler executes either way.
		err := conn.Invoke(ctx, "/does.not.Exist/Method", &grpc_health_v1.HealthCheckRequest{}, &grpc_health_v1.HealthCheckResponse{})
		c := status.Code(err)
		is.True(c == codes.Unauthenticated || c == codes.Unimplemented)
	})
	t.Run("health Check stays open, Watch does not", func(t *testing.T) {
		is := is.New(t)
		hc := grpc_health_v1.NewHealthClient(conn)
		_, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		is.NoErr(err)
		w, err := hc.Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
		is.NoErr(err)
		_, err = w.Recv()
		is.Equal(status.Code(err), codes.Unauthenticated)
	})
}

func TestServerAuth_RejectionIsCountedAndTokenNeverLogged(t *testing.T) {
	is := is.New(t)
	var sb strings.Builder
	rej := apisec.NewRejections(nil, log.New(zerologWriter(&sb)))
	am := NewAuthManager()
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(am.UnaryServerInterceptor(rej)))
	connutilsv1.RegisterSchemaServiceServer(srv, server.NewSchemaServiceServer(fakeSchemaService{}))
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	is.NoErr(err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(), "conduit-connector-token", "presented-secret-value")
	_, err = connutilsv1.NewSchemaServiceClient(conn).CreateSchema(ctx, &connutilsv1.CreateSchemaRequest{})
	is.Equal(status.Code(err), codes.Unauthenticated)
	is.True(strings.Contains(sb.String(), "api.unauthenticated"))
	is.True(!strings.Contains(sb.String(), "presented-secret-value"))
}
