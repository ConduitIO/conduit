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

package connutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// ErrInvalidToken is returned when a connector token is missing or unknown. It
// never carries the presented token.
var ErrInvalidToken = cerrors.New("invalid token")

// tokenValueBytes is the entropy of a connector token: 256 bits.
const tokenValueBytes = 32

type token struct {
	Value       string `json:"value"`
	ConnectorID string `json:"connector_id"`
}

// AuthManager issues and validates the per-connector tokens that standalone
// connector plugins present to the connector utilities server. Tokens are
// opaque strings to plugins. Only SHA-256 digests are stored, and a presented
// token is looked up by its digest, so neither the lookup time nor a dump of
// the map reveals or depends on any real token.
type AuthManager struct {
	tokens map[[sha256.Size]byte]struct{}
	m      sync.RWMutex
}

func NewAuthManager() *AuthManager {
	return &AuthManager{
		tokens: make(map[[sha256.Size]byte]struct{}),
	}
}

func digest(tokenStr string) [sha256.Size]byte { return sha256.Sum256([]byte(tokenStr)) }

// GenerateNew issues a token for connectorID: 256 bits from crypto/rand.
func (s *AuthManager) GenerateNew(connectorID string) string {
	raw := make([]byte, tokenValueBytes)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail on supported platforms; an unauthenticated
		// fallback would be worse than stopping.
		panic(cerrors.Errorf("failed to read random bytes for a connector token: %w", err))
	}

	tkn := token{
		Value:       hex.EncodeToString(raw),
		ConnectorID: connectorID,
	}

	bytes, err := json.Marshal(tkn)
	if err != nil {
		// token is a struct we use internally, with fields that are known in advance,
		// so an error isn't really expected.
		panic(cerrors.Errorf("failed to marshal token: %w", err))
	}

	tokenStr := string(bytes)

	s.m.Lock()
	defer s.m.Unlock()
	s.tokens[digest(tokenStr)] = struct{}{}

	return tokenStr
}

// IsTokenValid returns nil if token was issued and not deregistered. The error
// does not contain the token.
func (s *AuthManager) IsTokenValid(token string) error {
	s.m.RLock()
	defer s.m.RUnlock()

	if _, ok := s.tokens[digest(token)]; !ok {
		return ErrInvalidToken
	}

	return nil
}

// Deregister revokes token.
func (s *AuthManager) Deregister(token string) {
	s.m.Lock()
	defer s.m.Unlock()

	delete(s.tokens, digest(token))
}
