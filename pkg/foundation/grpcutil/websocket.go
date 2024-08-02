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
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
)

type inMemoryResponseWriter struct {
	io.Writer
	header http.Header
}

func newInMemoryResponseWriter(writer io.Writer) *inMemoryResponseWriter {
	return &inMemoryResponseWriter{
		Writer: writer,
		header: http.Header{},
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
func (w *inMemoryResponseWriter) Flush() {}

var (
	defaultWriteWait = 10 * time.Second
	defaultPongWait  = 60 * time.Second
)

// webSocketProxy is a proxy around a http.Handler which
// redirects the response data from the http.Handler
// to a WebSocket connection.
type webSocketProxy struct {
	// done is closed to indicate that the proxy should stop working
	done <-chan struct{}
	// handler is the underlying handler actually handling requests
	handler http.Handler
	logger  log.CtxLogger

	upgrader websocket.Upgrader
	// Time allowed to write a message to the peer.
	writeWait time.Duration
	// Time allowed to read the next pong message from the peer.
	pongWait time.Duration
	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod time.Duration
}

func newWebSocketProxy(ctx context.Context, handler http.Handler, logger log.CtxLogger) *webSocketProxy {
	proxy := &webSocketProxy{
		handler: handler,
		done:    ctx.Done(),
		logger:  logger.WithComponent("grpcutil.webSocketProxy"),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		writeWait:  defaultWriteWait,
		pongWait:   defaultPongWait,
		pingPeriod: (defaultPongWait * 9) / 10,
	}
	return proxy
}

func (p *webSocketProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		p.handler.ServeHTTP(w, r)
		return
	}
	p.proxy(w, r)
}

// proxy creates a "pipeline" from the underlying response
// to a WebSocket connection. The pipeline is constructed in
// the following way:
//
//	  underlying response
//		 -> inMemoryResponseWriter
//		   -> scanner
//		     -> messages channel
//		       -> connection writer
//
// In the case of an error due to which we need to abort the request
// and close the WebSocket connection, we need to cancel the request context
// and stop writing any data to the WebSocket connection. This will
// automatically halt all the "pipeline nodes" after the underlying response.
func (p *webSocketProxy) proxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancelCtx := context.WithCancel(r.Context())
	defer cancelCtx()
	r = r.WithContext(ctx)
	go func() {
		// we're using ChanOut.Recv() so that the goroutine doesn't wait on
		// the proxy to be done even when the request is done
		_, _, err := cchan.ChanOut[struct{}](p.done).Recv(ctx)
		p.logger.Debug(ctx).Err(err).Msgf("websocket connection will be closed")
		cancelCtx()
	}()

	// Upgrade connection to WebSocket
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

	// Start the "underlying" http.Handler
	go func() {
		p.handler.ServeHTTP(response, r)
		p.logger.Debug(ctx).Err(ctx.Err()).Msg("closing pipes")
		responseW.CloseWithError(io.EOF)
	}()

	messages := make(chan []byte)
	// startWebSocketRead and startWebSocketWrite need to cancel the context
	// if they encounter an error reading from or writing to the WS connection
	go p.startWebSocketRead(ctx, conn, cancelCtx)
	go p.readFromHTTPResponse(ctx, responseR, messages)
	p.startWebSocketWrite(ctx, messages, conn, cancelCtx)
}

// startWebSocketRead starts a read loop on the proxy's WebSocket connection.
// The read loop will stop if there's been an error reading a message.
func (p *webSocketProxy) startWebSocketRead(ctx context.Context, conn *websocket.Conn, onDone func()) {
	defer onDone()

	conn.SetReadLimit(512)
	err := conn.SetReadDeadline(time.Now().Add(p.pongWait))
	if err != nil {
		p.logger.Warn(ctx).Err(err).Msgf("couldn't set read deadline %v", p.pongWait)
		return
	}

	conn.SetPongHandler(func(string) error {
		err := conn.SetReadDeadline(time.Now().Add(p.pongWait))
		if err != nil {
			// todo return err?
			p.logger.Warn(ctx).Err(err).Msgf("couldn't set read deadline %v", p.pongWait)
		}
		return nil
	})

	for {
		// The only use we have for reads right now
		// is for ping, pong and close messages.
		// https://pkg.go.dev/github.com/gorilla/websocket#hdr-Control_Messages
		// Also, a read loop can detect client disconnects much quicker:
		// https://groups.google.com/g/golang-nuts/c/FFzQO26jEoE/m/mYhcsK20EwAJ
		_, _, err := conn.ReadMessage()
		if err != nil {
			if p.isClosedConnErr(err) {
				p.logger.Debug(ctx).Err(err).Msg("closed connection")
			} else {
				p.logger.Warn(ctx).Err(err).Msg("read error")
			}

			break
		}
	}
}

func (p *webSocketProxy) isClosedConnErr(err error) bool {
	str := err.Error()
	if strings.Contains(str, "use of closed network connection") {
		return true
	}
	return websocket.IsCloseError(
		err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure,
		websocket.CloseNoStatusReceived,
	)
}

func (p *webSocketProxy) readFromHTTPResponse(ctx context.Context, responseReader io.Reader, c chan []byte) {
	defer close(c)
	scanner := bufio.NewScanner(responseReader)

	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			p.logger.Warn(ctx).Err(scanner.Err()).Msg("[write] empty scan")
			continue
		}

		p.logger.Trace(ctx).Msgf("[write] scanned %v", scanner.Text())
		c <- scanner.Bytes()
	}

	if sErr := scanner.Err(); sErr != nil {
		p.logger.Err(ctx, sErr).Msg("failed reading data from original response")
		c <- []byte(sErr.Error())
	}

	p.logger.Debug(ctx).Msg("scanner reached end of input data")
}

func (p *webSocketProxy) startWebSocketWrite(ctx context.Context, messages chan []byte, conn *websocket.Conn, cancelFn func()) {
	ticker := time.NewTicker(p.pingPeriod)
	defer func() {
		ticker.Stop()
		cancelFn()
		for range messages {
			// throw away
		}
	}()

	for {
		select {
		case message, ok := <-messages:
			conn.SetWriteDeadline(time.Now().Add(p.writeWait)) //nolint:errcheck // always returns nil
			if !ok {
				// readFromHTTPResponse closed the channel.
				err := conn.WriteMessage(websocket.CloseMessage, []byte{})
				if err != nil {
					p.logger.Warn(ctx).Err(err).Msg("failed sending close message")
				}
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				// NB: if this connection has been closed by the client
				// then that will cancel the request context, which in turn
				// makes the request return `{"code":1,"message":"context canceled","details":[]}`.
				// This proxy will try to write that, but will fail,
				// because the connection has already been closed.
				e := p.logger.Warn(ctx)
				if string(message) == `{"code":1,"message":"context canceled","details":[]}` {
					e = p.logger.Trace(ctx)
				}
				e.Bytes("message", message).Err(err).Msg("failed writing websocket message")
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(p.writeWait)) //nolint:errcheck // always returns nil
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
