// Copyright © 2024 Meroxa, Inc.
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
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
	"github.com/google/uuid"
)

var ErrInvalidToken = cerrors.New("invalid token")

type token struct {
	Value       string `json:"value"`
	ConnectorID string `json:"connector_id"`
}

type AuthManager struct {
	tokens map[string]bool
	m      sync.RWMutex
}

func NewAuthManager() *AuthManager {
	return &AuthManager{
		tokens: make(map[string]bool),
	}
}

func (s *AuthManager) GenerateNew(connectorID string) string {
	s.m.Lock()
	defer s.m.Unlock()

	tkn := token{
		Value:       uuid.NewString(),
		ConnectorID: connectorID,
	}

	bytes, err := json.Marshal(tkn)
	if err != nil {
		// token is a struct we use internally, with fields that are known in advance,
		// so an error isn't really expected.
		panic(cerrors.Errorf("failed to marshal token: %w", err))
	}

	tokenStr := string(bytes)
	s.tokens[tokenStr] = true

	return tokenStr
}

func (s *AuthManager) IsTokenValid(token string) error {
	s.m.RLock()
	defer s.m.RUnlock()

	if _, ok := s.tokens[token]; !ok {
		return cerrors.Errorf("%q: %w", token, ErrInvalidToken)
	}

	return nil
}

func (s *AuthManager) Deregister(token string) {
	s.m.Lock()
	defer s.m.Unlock()

	delete(s.tokens, token)
}
