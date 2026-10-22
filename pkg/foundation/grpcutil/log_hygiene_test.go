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

package grpcutil

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/gorilla/websocket"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestWebSocket_TraceLogDoesNotContainMessage: the inspector streams record
// data over the websocket, so the trace log of a scanned message carries its
// size only.
func TestWebSocket_TraceLogDoesNotContainMessage(t *testing.T) {
	is := is.New(t)
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const secret = "record-SECRET-4711"
	var buf syncBuffer
	logger := log.New(zerolog.New(&buf).Level(zerolog.TraceLevel))

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(secret + "\n"))
		is.NoErr(err)
	})
	s := httptest.NewServer(newWebSocketProxy(context.Background(), h, logger, nil))
	defer s.Close()

	ws, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	is.NoErr(err)
	defer ws.Close()
	defer resp.Body.Close()

	_, got, err := ws.ReadMessage()
	is.NoErr(err)
	is.Equal(string(got), secret) // the client still gets the message

	_, _, err = ws.ReadMessage() // wait for the stream to end, so the log is complete
	is.True(err != nil)

	out := buf.String()
	is.True(strings.Contains(out, "[write] scanned message"))
	is.True(!strings.Contains(out, secret))
}

// TestWebSocket_FailedWriteLogDoesNotContainMessage: when writing to a client
// that went away fails, the warning carries the message size only.
func TestWebSocket_FailedWriteLogDoesNotContainMessage(t *testing.T) {
	is := is.New(t)
	const secret = "record-SECRET-4711"
	var buf syncBuffer
	p := newWebSocketProxy(context.Background(), http.NotFoundHandler(), log.New(zerolog.New(&buf).Level(zerolog.WarnLevel)), nil)

	p.logWriteFailure(context.Background(), []byte(secret), io.ErrClosedPipe)

	out := buf.String()
	is.True(strings.Contains(out, "failed writing websocket message"))
	is.True(strings.Contains(out, `"message_size":18`))
	is.True(!strings.Contains(out, secret))
}

// syncBuffer is a bytes.Buffer safe for the proxy's goroutines to write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
