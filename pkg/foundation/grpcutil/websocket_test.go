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
	"github.com/conduitio/conduit/pkg/foundation/cchan"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
	"github.com/matryer/is"
)

func TestWebSocket_NoUpgradeToWebSocket(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msg := "hi there"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(msg))
		is.NoErr(err)
	})
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
	is.Equal(msg, string(bytes))
}

func TestWebSocket_Read(t *testing.T) {
	is := is.New(t)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Data written to a WebSocket is new-line delimited
		_, err := w.Write([]byte("hi there\n"))
		is.NoErr(err)
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

	_, _, err = ws.ReadMessage()
	is.True(err != nil)

	err = ws.Close()
	is.NoErr(err)
}

func TestWebSocket_Read_ClientClosed(t *testing.T) {
	is := is.New(t)

	handlerDone := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		// Data written to a WebSocket is new-line delimited
		_, err := w.Write([]byte("hi there\n"))
		is.NoErr(err)
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

	err = ws.Close()
	is.NoErr(err)

	_, ok, err := cchan.Chan[struct{}](handlerDone).RecvTimeout(context.Background(), time.Second)
	is.True(!ok) // expected channel to be closed
	is.NoErr(err)
}
