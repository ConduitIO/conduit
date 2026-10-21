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

// Package egresstest builds egress guards for tests that talk to a local
// httptest server. Production code must not import it.
package egresstest

import (
	"net"
	"net/http/httptest"
	"testing"

	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
)

// Guard returns a DialGuard that exempts exactly the address srv listens on
// and nothing else.
func Guard(tb testing.TB, srv *httptest.Server) *egress.DialGuard {
	tb.Helper()
	return GuardForAddr(tb, srv.Listener.Addr().String())
}

// GuardForAddr returns a DialGuard that exempts exactly host:port.
func GuardForAddr(tb testing.TB, hostPort string) *egress.DialGuard {
	tb.Helper()
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		tb.Fatalf("egresstest: bad address %q: %v", hostPort, err)
	}
	e, err := egress.ParseAllowEntry("http://" + hostPort)
	if err != nil {
		tb.Fatalf("egresstest: %v", err)
	}
	return egress.NewDialGuard([]egress.AllowEntry{e})
}

// MustGuardForAddr is GuardForAddr for Example functions, which have no
// testing.TB. It panics on a malformed address.
func MustGuardForAddr(hostPort string) *egress.DialGuard {
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		panic("egresstest: bad address " + hostPort + ": " + err.Error())
	}
	e, err := egress.ParseAllowEntry("http://" + hostPort)
	if err != nil {
		panic("egresstest: " + err.Error())
	}
	return egress.NewDialGuard([]egress.AllowEntry{e})
}
