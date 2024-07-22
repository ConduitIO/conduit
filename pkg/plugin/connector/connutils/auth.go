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

type Token struct {
	// todo need better name
	Token       string `json:"token"`
	ConnectorID string `json:"connector_id"`
}

type TokenService struct {
	tokens map[string]bool
	m      sync.Mutex
}

func NewTokenService() *TokenService {
	return &TokenService{
		tokens: make(map[string]bool),
	}
}

func (s *TokenService) GenerateNew(connectorID string) string {
	s.m.Lock()
	defer s.m.Unlock()

	token := Token{
		Token:       uuid.NewString(),
		ConnectorID: connectorID,
	}

	bytes, err := json.Marshal(token)
	if err != nil {
		panic(cerrors.Errorf("failed to marshal token: %w", err))
	}

	tokenStr := string(bytes)
	s.tokens[tokenStr] = true

	return tokenStr
}

func (s *TokenService) IsTokenValid(token string) error {
	s.m.Lock()
	defer s.m.Unlock()

	if _, ok := s.tokens[token]; !ok {
		return cerrors.Errorf("%q: %w", token, ErrInvalidToken)
	}

	return nil
}

func (s *TokenService) Deregister(token string) {
	s.m.Lock()
	defer s.m.Unlock()

	delete(s.tokens, token)
}
