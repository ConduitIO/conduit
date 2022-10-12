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
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// GlobalBuilderRegistry is a global registry of processor builders. It should
// be treated as a read only variable.
var GlobalBuilderRegistry = NewBuilderRegistry()

// Builder parses the config and if valid returns a processor, an error otherwise.
type Builder func(Config) (Interface, error)

// BuilderRegistry is a registry for registering or looking up processor
// builders. The Register and Get methods are safe for concurrent use.
type BuilderRegistry struct {
	builders map[string]Builder

	lock sync.RWMutex
}

// NewBuilderRegistry returns an empty *BuilderRegistry.
func NewBuilderRegistry() *BuilderRegistry {
	return &BuilderRegistry{
		builders: make(map[string]Builder),
	}
}

// MustRegister tries to register a builder and panics on error.
func (r *BuilderRegistry) MustRegister(procType string, b Builder) {
	err := r.Register(procType, b)
	if err != nil {
		panic(cerrors.Errorf("register processor builder failed: %w", err))
	}
}

// Register registers a processor builder under the specified type.
// If a builder is already registered under that type it returns an error.
func (r *BuilderRegistry) Register(procType string, b Builder) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	if _, ok := r.builders[procType]; ok {
		return cerrors.Errorf("processor builder with type %q already registered", procType)
	}
	r.builders[procType] = b

	return nil
}

// MustGet tries to get a builder and panics on error.
func (r *BuilderRegistry) MustGet(procType string) Builder {
	b, err := r.Get(procType)
	if err != nil {
		panic(cerrors.Errorf("get processor builder failed: %w", err))
	}
	return b
}

// Get returns the processor builder registered under the specified type.
// If no builder is registered under that type it returns an error.
func (r *BuilderRegistry) Get(procType string) (Builder, error) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	b, ok := r.builders[procType]
	if !ok {
		return nil, cerrors.Errorf("processor builder %q not found", procType)
	}

	return b, nil
}
