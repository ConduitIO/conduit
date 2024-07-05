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

	"github.com/conduitio/conduit-connector-protocol/pconduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry/fromschema"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/twmb/franz-go/pkg/sr"
)

type Service struct {
	registry Registry
	logger   log.CtxLogger
}

var _ pconduit.SchemaService = (*Service)(nil)

func NewService(logger log.CtxLogger, registry Registry) *Service {
	return &Service{
		registry: registry,
		logger:   logger,
	}
}

func (s *Service) Check(ctx context.Context) error {
	r, ok := s.registry.(RegistryWithCheck)
	if !ok {
		return nil
	}
	return r.Check(ctx)
}

func (s *Service) CreateSchema(ctx context.Context, req pconduit.CreateSchemaRequest) (pconduit.CreateSchemaResponse, error) {
	ss, err := s.registry.CreateSchema(ctx, req.Subject, sr.Schema{
		Schema: string(req.Bytes),
		Type:   fromschema.SrSchemaType(req.Type),
	})
	if err != nil {
		// TODO: convert error
		return pconduit.CreateSchemaResponse{}, cerrors.Errorf("failed to create schema: %w", err)
	}
	return pconduit.CreateSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}

func (s *Service) GetSchema(ctx context.Context, req pconduit.GetSchemaRequest) (pconduit.GetSchemaResponse, error) {
	ss, err := s.registry.SchemaBySubjectVersion(ctx, req.Subject, req.Version)
	if err != nil {
		// TODO: convert error
		return pconduit.GetSchemaResponse{}, cerrors.Errorf("failed to get schema by subject and version: %w", err)
	}

	return pconduit.GetSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}
