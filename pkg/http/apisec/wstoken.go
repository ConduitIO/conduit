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

package apisec

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

const (
	// WebSocketProtocol is the subprotocol Conduit selects on every WebSocket
	// upgrade. A browser requires the server to pick one of the protocols it
	// offered, so the token carrier is never the one echoed back.
	WebSocketProtocol = "conduit"

	// WebSocketTokenPrefix starts the carrier subprotocol: the browser offers
	// "conduit" and "conduit.token.<base64url(token)>" in
	// Sec-WebSocket-Protocol, because the WebSocket API cannot set headers and
	// a token in the URL would end up in logs and history.
	WebSocketTokenPrefix = "conduit.token."
)

// isWebSocketUpgrade decides whether r is a WebSocket handshake. It must agree
// with the proxy that performs the upgrade (grpcutil.webSocketProxy uses the
// same function), or a request could be authenticated as a handshake here and
// served as a plain request there. The carrier is only honored on GET, the only
// method a handshake may use.
func isWebSocketUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet && websocket.IsWebSocketUpgrade(r)
}

// EncodeWebSocketToken returns the carrier subprotocol for token.
func EncodeWebSocketToken(token string) string {
	return WebSocketTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(token))
}

// takeWebSocketToken extracts the token from the offered subprotocols and
// rewrites Sec-WebSocket-Protocol to hold only the non-carrier entries, so the
// token goes no further than this function. It returns ok=false if no
// well-formed carrier was offered.
func takeWebSocketToken(r *http.Request) (string, bool) {
	var (
		token string
		found bool
		keep  []string
	)
	for _, line := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(line, ",") {
			p = strings.TrimSpace(p)
			enc, isCarrier := strings.CutPrefix(p, WebSocketTokenPrefix)
			if !isCarrier {
				if p != "" {
					keep = append(keep, p)
				}
				continue
			}
			if b, err := base64.RawURLEncoding.DecodeString(enc); err == nil && !found {
				token, found = string(b), true
			}
		}
	}
	r.Header.Del("Sec-WebSocket-Protocol")
	if len(keep) > 0 {
		r.Header.Set("Sec-WebSocket-Protocol", strings.Join(keep, ", "))
	}
	return token, found
}
