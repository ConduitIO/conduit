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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/schemaregistry"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"strconv"
)

type Service struct {
	fakeReg *schemaregistry.FakeRegistry
}

func NewService() *Service {
	fr := &schemaregistry.FakeRegistry{}
	fr.Init()

	return &Service{fakeReg: fr}
}

func (s *Service) Register(ctx context.Context, inst Instance) (string, error) {
	created := s.fakeReg.CreateSchema(inst.Name, sr.Schema{
		Schema: string(inst.Bytes),
		Type:   sr.TypeAvro,
	})

	return strconv.Itoa(created.ID), nil
}

func (s *Service) Fetch(ctx context.Context, id string) (Instance, error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return Instance{}, cerrors.Errorf("invalid instance id: %w", err)
	}

	schema, found := s.fakeReg.SchemaByID(idInt)
	if !found {
		return Instance{}, cerrors.New("schema not found")
	}

	return Instance{
		ID:      id,
		Name:    "",
		Version: 0,
		Type:    TypeAvro,
		Bytes:   []byte(schema.Schema),
	}, nil
}
