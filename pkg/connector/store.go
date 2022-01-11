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

package connector

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const (
	// storeKeyPrefix is added to all keys before storing them in store. Do not
	// change unless you know what you're doing and you have a migration plan in
	// place.
	storeKeyPrefix = "connector:connector:"
)

// Store handles the persistence and fetching of connectors.
type Store struct {
	db          database.DB
	logger      log.CtxLogger
	connBuilder Builder
}

func NewStore(db database.DB, logger log.CtxLogger, builder Builder) *Store {
	return &Store{
		db:          db,
		logger:      logger.WithComponent("connector.Store"),
		connBuilder: builder,
	}
}

// Set stores connector under the key id and returns nil on success, error
// otherwise.
func (s *Store) Set(ctx context.Context, id string, c Connector) error {
	if id == "" {
		return cerrors.Errorf("can't store connector: %w", cerrors.ErrEmptyID)
	}

	raw, err := s.encode(c)
	if err != nil {
		return err
	}
	key := s.addKeyPrefix(id)

	err = s.db.Set(ctx, key, raw)
	if err != nil {
		return cerrors.Errorf("failed to store connector with ID %q: %w", id, err)
	}

	return nil
}

// Delete deletes connector under the key id and returns nil on success, error
// otherwise.
func (s *Store) Delete(ctx context.Context, id string) error {
	if id == "" {
		return cerrors.Errorf("can't delete connector: %w", cerrors.ErrEmptyID)
	}

	key := s.addKeyPrefix(id)

	err := s.db.Set(ctx, key, nil)
	if err != nil {
		return cerrors.Errorf("failed to delete connector with ID %q: %w", id, err)
	}

	return nil
}

// Get will return the connector for a given id or an error.
func (s *Store) Get(ctx context.Context, id string) (Connector, error) {
	key := s.addKeyPrefix(id)

	raw, err := s.db.Get(ctx, key)
	if err != nil {
		return nil, cerrors.Errorf("failed to get connector with ID %q: %w", id, err)
	}
	if len(raw) == 0 {
		return nil, cerrors.Errorf("database returned empty connector for ID %q", id)
	}

	return s.decode(raw)
}

// GetAll returns all connectors stored in the database.
func (s *Store) GetAll(ctx context.Context) (map[string]Connector, error) {
	prefix := s.addKeyPrefix("")
	keys, err := s.db.GetKeys(ctx, prefix)
	if err != nil {
		return nil, cerrors.Errorf("failed to retrieve keys: %w", err)
	}
	connectors := make(map[string]Connector)
	for _, key := range keys {
		raw, err := s.db.Get(ctx, key)
		if err != nil {
			return nil, cerrors.Errorf("failed to get connector with ID %q: %w", key, err)
		}
		c, err := s.decode(raw)
		if err != nil {
			return nil, cerrors.Errorf("failed to decode connector with ID %q: %w", key, err)
		}
		connectors[s.trimKeyPrefix(key)] = c
	}

	return connectors, nil
}

// store is namespaced, meaning that keys all have the same prefix.
// You can pass this a blank string to get the prefix key for all connectors.
func (*Store) addKeyPrefix(id string) string {
	return storeKeyPrefix + id
}

func (*Store) trimKeyPrefix(key string) string {
	return strings.TrimPrefix(key, storeKeyPrefix)
}

// encode a connector from Connector to []byte.
func (*Store) encode(c Connector) ([]byte, error) {
	locker, ok := c.(sync.Locker)
	if ok {
		// a connector can choose to implement the locker interface, then the
		// store will make sure to first acquire the lock before encoding it to
		// prevent race conditions
		locker.Lock()
		defer locker.Unlock()
	}

	tempJSON, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	typedConnector := typedJSON{Data: tempJSON}
	switch c.Type() {
	case TypeDestination:
		typedConnector.Type = "destination"
	case TypeSource:
		typedConnector.Type = "source"
	}

	b, err := json.Marshal(typedConnector)
	if err != nil {
		return nil, err
	}

	return b, nil
}

// decode a connector from []byte to Connector. It uses the Builder to
// initialize the connector making it ready to be used.
func (s *Store) decode(raw []byte) (Connector, error) {
	var typedConnector typedJSON
	err := json.Unmarshal(raw, &typedConnector)
	if err != nil {
		return nil, err
	}

	var conn Connector
	switch typedConnector.Type {
	case "source":
		conn, err = s.connBuilder.Build(TypeSource)
		if err != nil {
			return nil, err
		}
	case "destination":
		conn, err = s.connBuilder.Build(TypeDestination)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrInvalidConnectorType
	}

	err = json.Unmarshal(typedConnector.Data, &conn)
	if err != nil {
		return nil, err
	}

	if err := s.connBuilder.Init(conn, conn.ID(), conn.Config()); err != nil {
		return nil, err
	}
	return conn, nil
}

// typedJSON stores a connector type and the marshaled connector
// will be used as an intermediate representation for the connectors, to help with marshaling and unmarshalling them
type typedJSON struct {
	Type string
	Data json.RawMessage
}
