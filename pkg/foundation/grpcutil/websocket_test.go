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
	"time"

	"github.com/conduitio/conduit-commons/cchan"
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
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), nil))
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

// TestWebSocket_CheckOrigin_Wired proves the checkOrigin passed to
// newWebSocketProxy actually gates the upgrade: a disallowed Origin is rejected
// with 403, an allowed one (and none at all) upgrades. This is the wiring the
// CORS allowlist relies on — a websocket upgrade never passes through the HTTP
// CORS middleware, so without this guard gorilla's default would still reject
// every cross-origin stream.
func TestWebSocket_CheckOrigin_Wired(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("hi\n"))
		is.NoErr(err)
	})
	const allow = "http://allowed.example"
	check := func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		return o == "" || o == allow
	}
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), check))
	defer s.Close()
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	// Disallowed origin -> handshake refused with 403.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://evil.example"}})
	is.True(err != nil)
	if resp != nil {
		is.Equal(resp.StatusCode, http.StatusForbidden)
		resp.Body.Close()
	}

	// Allowed origin -> upgrades.
	ws, resp2, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {allow}})
	is.NoErr(err)
	defer ws.Close()
	defer resp2.Body.Close()
}

func TestWebSocket_Read_Single(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Data written to a WebSocket is new-line delimited
		_, err := w.Write([]byte("hi there\n"))
		is.NoErr(err)
	})
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), nil))
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

func TestWebSocket_Read_Multiple(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Data written to a WebSocket is new-line delimited
		_, err := w.Write([]byte("first message\n"))
		is.NoErr(err)

		_, err = w.Write([]byte("second message\n"))
		is.NoErr(err)
	})
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), nil))
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
	is.Equal("first message", string(bytes))
	is.Equal(websocket.TextMessage, msgType)

	msgType, bytes, err = ws.ReadMessage()
	is.NoErr(err)
	is.Equal("second message", string(bytes))
	is.Equal(websocket.TextMessage, msgType)

	_, _, err = ws.ReadMessage()
	is.True(err != nil)

	err = ws.Close()
	is.NoErr(err)
}

func TestWebSocket_Read_ClientClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	handlerDone := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		<-r.Context().Done()
	})
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), nil))
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

	_, ok, err := cchan.ChanOut[struct{}](handlerDone).RecvTimeout(context.Background(), time.Second)
	is.True(!ok) // expected channel to be closed
	is.NoErr(err)
}

func TestWebSocket_ContextCancelled(t *testing.T) {
	is := is.New(t)
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	handlerDone := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		<-r.Context().Done()
	})
	s := httptest.NewServer(newWebSocketProxy(ctx, h, log.Nop(), nil))
	defer s.Close()

	// Convert http to ws
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	// Connect to the server
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	is.NoErr(err)
	defer ws.Close()
	defer resp.Body.Close()

	cancelCtx()

	_, ok, err := cchan.ChanOut[struct{}](handlerDone).RecvTimeout(context.Background(), time.Second)
	is.True(!ok) // expected channel to be closed
	is.NoErr(err)
}
