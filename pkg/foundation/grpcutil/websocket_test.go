// Copyright © 2022 Meroxa, Inc.
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

package grpcutil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

type testHandler struct {
	is       *is.I
	response string
}

func (h *testHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	_, err := w.Write([]byte(h.response))
	h.is.NoErr(err)
}

func TestWebSocket_NoUpgradeToWebSocket(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := &testHandler{
		is:       is,
		response: "hi there",
	}
	s := httptest.NewServer(newWebSocketProxy(h, log.Nop()))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	is.NoErr(err)

	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	is.True(resp.Body != nil) // expected response to have a body
	defer resp.Body.Close()

	bytes, err := io.ReadAll(resp.Body)
	is.NoErr(err)
	is.Equal(h.response, string(bytes))
}
