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

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
)

const (
	// storeKeyPrefix is added to all keys before storing them in store. Do not
	// change unless you know what you're doing and you have a migration plan in
	// place.
	storeKeyPrefix = "processor:instance:"
)

// Store handles the persistence and fetching of processor instances.
type Store struct {
	db       database.DB
	registry *BuilderRegistry
}

func NewStore(db database.DB, registry *BuilderRegistry) *Store {
	return &Store{
		db:       db,
		registry: registry,
	}
}

// Set stores instance under the key id and returns nil on success, error
// otherwise.
func (s *Store) Set(ctx context.Context, id string, instance *Instance) error {
	if id == "" {
		return cerrors.Errorf("can't store processor instance: %w", cerrors.ErrEmptyID)
	}

	raw, err := s.encode(instance)
	if err != nil {
		return err
	}
	key := s.addKeyPrefix(id)

	err = s.db.Set(ctx, key, raw)
	if err != nil {
		return cerrors.Errorf("failed to store processor instance with ID %q: %w", id, err)
	}

	return nil
}

// Delete deletes instance under the key id and returns nil on success, error
// otherwise.
func (s *Store) Delete(ctx context.Context, id string) error {
	if id == "" {
		return cerrors.Errorf("can't delete processor instance: %w", cerrors.ErrEmptyID)
	}

	key := s.addKeyPrefix(id)

	err := s.db.Set(ctx, key, nil)
	if err != nil {
		return cerrors.Errorf("failed to delete processor instance with ID %q: %w", id, err)
	}

	return nil
}

// Get will return the processor instance for a given id or an error.
func (s *Store) Get(ctx context.Context, id string) (*Instance, error) {
	key := s.addKeyPrefix(id)

	raw, err := s.db.Get(ctx, key)
	if err != nil {
		return nil, cerrors.Errorf("failed to get processor instance with ID %q: %w", id, err)
	}
	if len(raw) == 0 {
		return nil, cerrors.Errorf("database returned empty processor instance for ID %q", id)
	}

	return s.decode(raw)
}

// GetAll returns all instances stored in the database.
func (s *Store) GetAll(ctx context.Context) (map[string]*Instance, error) {
	prefix := s.addKeyPrefix("")
	keys, err := s.db.GetKeys(ctx, prefix)
	if err != nil {
		return nil, cerrors.Errorf("failed to retrieve keys: %w", err)
	}
	instances := make(map[string]*Instance)
	for _, key := range keys {
		raw, err := s.db.Get(ctx, key)
		if err != nil {
			return nil, cerrors.Errorf("failed to get processor instance with ID %q: %w", key, err)
		}
		instance, err := s.decode(raw)
		if err != nil {
			return nil, cerrors.Errorf("failed to decode processor instance with ID %q: %w", key, err)
		}
		instances[s.trimKeyPrefix(key)] = instance
	}

	return instances, nil
}

// Store is namespaced, meaning that keys all have the same prefix.
// You can pass this a blank string to get the prefix key for all instances.
func (*Store) addKeyPrefix(id string) string {
	return storeKeyPrefix + id
}

func (*Store) trimKeyPrefix(key string) string {
	return strings.TrimPrefix(key, storeKeyPrefix)
}

// encode encodes a instance from *Instance to []byte. It uses storeInstance in
// the background to encode the instance including the processor type.
func (*Store) encode(instance *Instance) ([]byte, error) {
	i := *instance    // create copy of instance as to not modify it
	i.Processor = nil // do not persist processor

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	err := enc.Encode(i)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decode decodes a instance from []byte to *Instance. It uses storeInstance in
// the background to decode the processor type and create an instance with a the
// correct processor.
func (s *Store) decode(raw []byte) (*Instance, error) {
	var i Instance
	r := bytes.NewReader(raw)
	dec := json.NewDecoder(r)
	err := dec.Decode(&i)
	if err != nil {
		return nil, err
	}

	builder, err := s.registry.Get(i.Type)
	if err != nil {
		return nil, cerrors.Errorf("could not get processor builder for instance %s: %w", i.ID, err)
	}

	proc, err := builder(i.Config)
	if err != nil {
		return nil, cerrors.Errorf("could not create processor: %w", err)
	}

	i.Processor = proc
	return &i, nil
}
