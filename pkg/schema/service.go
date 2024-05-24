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

package schema

import (
	"context"
	"strconv"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/schemaregistry"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

var (
	ErrSchemaNotFound = cerrors.New("schema not found")
)

type Service struct {
	fakeReg *schemaregistry.FakeRegistry
}

func NewService() *Service {
	fr := &schemaregistry.FakeRegistry{}
	fr.Init()

	return &Service{fakeReg: fr}
}

func (s *Service) Check(ctx context.Context) error {
	return nil
}

func (s *Service) Create(_ context.Context, inst schema.Instance) (schema.Instance, error) {
	created := s.fakeReg.CreateSchema(inst.Name, sr.Schema{
		Schema: string(inst.Bytes),
		Type:   sr.TypeAvro,
	})

	return schema.Instance{
		ID:      strconv.Itoa(created.ID),
		Name:    "",
		Version: 0,
		Type:    schema.TypeAvro,
		Bytes:   []byte(created.Schema.Schema),
	}, nil
}

func (s *Service) Get(_ context.Context, id string) (schema.Instance, error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return schema.Instance{}, cerrors.Errorf("invalid schema id: %w", err)
	}

	sch, found := s.fakeReg.SchemaByID(idInt)
	if !found {
		return schema.Instance{}, ErrSchemaNotFound
	}

	return schema.Instance{
		ID:      id,
		Name:    "",
		Version: 0,
		Type:    schema.TypeAvro,
		Bytes:   []byte(sch.Schema),
	}, nil
}
