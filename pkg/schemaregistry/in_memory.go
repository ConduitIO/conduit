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

package schemaregistry

import (
	"context"
	"strconv"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type InMemoryService struct {
	reg *InMemoryRegistry
}

func NewInMemoryService() *InMemoryService {
	return &InMemoryService{reg: NewInMemoryRegistry()}
}

func (s *InMemoryService) Check(context.Context) error {
	return nil
}

func (s *InMemoryService) Create(_ context.Context, name string, bytes []byte) (schema.Instance, error) {
	created := s.reg.CreateSchema(name, sr.Schema{
		Schema: string(bytes),
		Type:   sr.TypeAvro,
	})

	return schema.Instance{
		ID:      strconv.Itoa(created.ID),
		Name:    created.Subject,
		Version: int32(created.Version),
		Type:    schema.TypeAvro,
		Bytes:   []byte(created.Schema.Schema),
	}, nil
}

// todo returned schema instance doesn't contain name and version
func (s *InMemoryService) Get(_ context.Context, id string) (schema.Instance, error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return schema.Instance{}, cerrors.Errorf("invalid schema id: %w", err)
	}

	sch, found := s.reg.SchemaByID(idInt)
	if !found {
		return schema.Instance{}, ErrSchemaNotFound
	}

	return schema.Instance{
		ID:    id,
		Type:  schema.TypeAvro,
		Bytes: []byte(sch.Schema),
	}, nil
}