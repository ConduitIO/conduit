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

	"github.com/conduitio/conduit-commons/schema"
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

func (s *InMemoryService) Create(_ context.Context, name string, bytes []byte) (schema.Schema, error) {
	created := s.reg.CreateSchema(name, sr.Schema{
		Schema: string(bytes),
		Type:   sr.TypeAvro,
	})

	return schema.Schema{
		Subject: created.Subject,
		Version: created.Version,
		Type:    schema.TypeAvro,
		Bytes:   []byte(created.Schema.Schema),
	}, nil
}

func (s *InMemoryService) Get(_ context.Context, name string, version int) (schema.Schema, error) {
	sch, found := s.reg.SchemaBySubjectVersion(name, version)
	if !found {
		return schema.Schema{}, ErrSchemaNotFound
	}

	return schema.Schema{
		Type:    schema.TypeAvro,
		Subject: sch.Subject,
		Version: sch.Version,
		Bytes:   []byte(sch.Schema.Schema),
	}, nil
}
