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

package egresstest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conduitio/conduit/pkg/plugin/processor/egress/egresstest"
	"github.com/matryer/is"
)

// TestGuard_ExemptsOnlyTheServer: the guard reaches the test server it was
// built for and still refuses another loopback port.
func TestGuard_ExemptsOnlyTheServer(t *testing.T) {
	is := is.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewServer(handler)
	defer srv.Close()
	other := httptest.NewServer(handler)
	defer other.Close()

	c := egresstest.Guard(t, srv).HTTPClient(0)
	status, err := get(t, c, srv.URL)
	is.NoErr(err)
	is.Equal(status, http.StatusNoContent)

	_, err = get(t, c, other.URL)
	is.True(err != nil) // a different loopback port is not exempt
}

// TestMustGuardForAddr builds a guard for a valid address and panics on a
// malformed one.
func TestMustGuardForAddr(t *testing.T) {
	is := is.New(t)
	is.True(egresstest.MustGuardForAddr("127.0.0.1:8080") != nil)
	is.True(egresstest.GuardForAddr(t, "127.0.0.1:8080") != nil)

	defer func() { is.True(recover() != nil) }()
	egresstest.MustGuardForAddr("no-port")
}

// fatalRecorder records Fatalf instead of stopping the test, so the
// bad-address path of GuardForAddr can be checked.
type fatalRecorder struct {
	testing.TB
	fatals int
}

func (r *fatalRecorder) Helper()               {}
func (r *fatalRecorder) Fatalf(string, ...any) { r.fatals++ }

// TestGuardForAddr_BadAddressFails: a malformed address fails the test rather
// than producing a guard that silently exempts nothing.
func TestGuardForAddr_BadAddressFails(t *testing.T) {
	is := is.New(t)
	r := &fatalRecorder{TB: t}
	egresstest.GuardForAddr(r, "no-port")
	is.True(r.fatals > 0)
}

// get issues a GET with a context and closes the body, so a refused request
// returns only its error.
func get(t *testing.T, c *http.Client, url string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
