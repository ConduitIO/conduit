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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
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

func TestWebSocket_UpgradeToWebSocket(t *testing.T) {
	is := is.New(t)
	// ctx := context.Background()

	// handlerDone := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// defer close(handlerDone)
		_, err := w.Write([]byte("hi there\n"))
		is.NoErr(err)
		// <-r.Context().Done()
	})
	s := httptest.NewServer(newWebSocketProxy(h, log.Nop()))
	defer s.Close()

	// Convert http to ws
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	// Connect to the server
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	is.NoErr(err)
	defer ws.Close()
	defer resp.Body.Close()

	msgType, bytes, err := ws.ReadMessage()
	is.NoErr(err)
	is.Equal("hi there", string(bytes))
	is.Equal(websocket.TextMessage, msgType)

	// _, _, err = cchan.Chan[struct{}](handlerDone).RecvTimeout(ctx, time.Millisecond*10)
	// is.Equal(err, context.DeadlineExceeded)

	_, _, err = ws.ReadMessage()
	is.True(err != nil)
	t.Log(err)

	// err = ws.Close()
	// is.NoErr(err)

	// _, ok, err := cchan.Chan[struct{}](handlerDone).RecvTimeout(ctx, time.Second)
	// is.True(!ok) // expected channel to be closed
	// is.NoErr(err)
}
