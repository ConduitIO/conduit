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
	"testing"
	"time"

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
