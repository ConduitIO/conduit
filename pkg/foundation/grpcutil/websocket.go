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
	"bufio"
	"context"
	"io"
	"net/http"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
)

type inMemoryResponseWriter struct {
	io.Writer
	header http.Header
	closed chan bool
}

func newInMemoryResponseWriter(writer io.Writer) *inMemoryResponseWriter {
	return &inMemoryResponseWriter{
		Writer: writer,
		header: http.Header{},
		closed: make(chan bool, 1),
	}
}

func (w *inMemoryResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}
func (w *inMemoryResponseWriter) Header() http.Header {
	return w.header
}
func (w *inMemoryResponseWriter) WriteHeader(int) {
	// we don't have a use for the code
}
func (w *inMemoryResponseWriter) CloseNotify() <-chan bool {
	return w.closed
}
func (w *inMemoryResponseWriter) Flush() {}

// webSocketProxy is a proxy around a http.Handler which
// redirects the response data from the http.Handler
// to a WebSocket connection.
type webSocketProxy struct {
	handler  http.Handler
	logger   log.CtxLogger
	upgrader websocket.Upgrader
}

func newWebSocketProxy(handler http.Handler, logger log.CtxLogger) *webSocketProxy {
	return &webSocketProxy{
		handler: handler,
		logger:  logger.WithComponent("grpcutil.webSocketProxy"),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

func (p *webSocketProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		p.handler.ServeHTTP(w, r)
		return
	}
	p.proxy(w, r)
}

func (p *webSocketProxy) proxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancelFn := context.WithCancel(r.Context())
	defer cancelFn()

	// upgrade connection to WebSocket
	conn, err := p.upgrader.Upgrade(w, r, http.Header{})
	if err != nil {
		p.logger.Err(ctx, err).Msg("error upgrading websocket")
		return
	}
	defer conn.Close()

	// We use a pipe to read the data being written to the underlying http.Handler
	// and then write it to the WebSocket connection.
	responseR, responseW := io.Pipe()
	response := newInMemoryResponseWriter(responseW)
	go func() {
		<-ctx.Done()
		p.logger.Debug(ctx).Err(ctx.Err()).Msg("closing pipes")
		responseW.CloseWithError(io.EOF)
		response.closed <- true
	}()

	go func() {
		defer cancelFn()
		p.handler.ServeHTTP(response, r)
	}()

	scanner := bufio.NewScanner(responseR)

	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			p.logger.Warn(ctx).Err(scanner.Err()).Msg("[write] empty scan")
			continue
		}

		p.logger.Trace(ctx).Msgf("[write] scanned %v", scanner.Text())
		if err := conn.WriteMessage(websocket.TextMessage, scanner.Bytes()); err != nil {
			p.logger.Warn(ctx).Err(err).Msg("[write] error writing websocket message")
			return
		}
	}

	if sErr := scanner.Err(); sErr != nil {
		p.logger.Err(ctx, sErr).Msg("failed reading data from original response")
		if err := conn.WriteMessage(websocket.TextMessage, []byte(sErr.Error())); err != nil {
			p.logger.Warn(ctx).Err(err).Msg("[write] failed writing scanner error")
		}
	}
}
