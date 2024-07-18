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
	"context"

	"github.com/conduitio/conduit-connector-protocol/pconduit"
	conduitschemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/fromschema"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/sr"
)

type SchemaService struct {
	registry schemaregistry.Registry
	logger   log.CtxLogger
	token    string
}

var _ pconduit.SchemaService = (*SchemaService)(nil)

func NewSchemaService(logger log.CtxLogger, registry schemaregistry.Registry) *SchemaService {
	return &SchemaService{
		registry: registry,
		logger:   logger.WithComponent("connutils.SchemaService"),
		token:    uuid.NewString(),
	}
}

func (s *SchemaService) Check(ctx context.Context) error {
	r, ok := s.registry.(schemaregistry.RegistryWithCheck)
	if !ok {
		return nil
	}
	return r.Check(ctx)
}

func (s *SchemaService) CreateSchema(ctx context.Context, req pconduit.CreateSchemaRequest) (pconduit.CreateSchemaResponse, error) {
	err := s.validateToken(ctx)
	if err != nil {
		return pconduit.CreateSchemaResponse{}, err
	}

	ss, err := s.registry.CreateSchema(ctx, req.Subject, sr.Schema{
		Schema: string(req.Bytes),
		Type:   fromschema.SrSchemaType(req.Type),
	})
	if err != nil {
		var respErr *sr.ResponseError
		if cerrors.As(err, &respErr) {
			return pconduit.CreateSchemaResponse{}, unwrapSrError(respErr) // don't wrap response errors
		}
		return pconduit.CreateSchemaResponse{}, cerrors.Errorf("failed to create schema: %w", err)
	}
	return pconduit.CreateSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}

func (s *SchemaService) GetSchema(ctx context.Context, req pconduit.GetSchemaRequest) (pconduit.GetSchemaResponse, error) {
	err := s.validateToken(ctx)
	if err != nil {
		return pconduit.GetSchemaResponse{}, err
	}

	ss, err := s.registry.SchemaBySubjectVersion(ctx, req.Subject, req.Version)
	if err != nil {
		var respErr *sr.ResponseError
		if cerrors.As(err, &respErr) {
			return pconduit.GetSchemaResponse{}, unwrapSrError(respErr) // don't wrap response errors
		}
		return pconduit.GetSchemaResponse{}, cerrors.Errorf("failed to get schema by subject and version: %w", err)
	}

	return pconduit.GetSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}

func (s *SchemaService) Token() string {
	return s.token
}

func (s *SchemaService) validateToken(ctx context.Context) error {
	token := pconduit.ConnectorTokenFromContext(ctx)
	if token != s.Token() {
		return cerrors.Errorf("token %q is invalid", token)
	}

	return nil
}

func unwrapSrError(e *sr.ResponseError) error {
	switch e.ErrorCode {
	case conduitschemaregistry.ErrorCodeSubjectNotFound,
		conduitschemaregistry.ErrorCodeVersionNotFound,
		conduitschemaregistry.ErrorCodeSchemaNotFound:
		return pconduit.ErrSchemaNotFound
	case conduitschemaregistry.ErrorCodeInvalidSchema:
		return pconduit.ErrInvalidSchemaBytes // TODO change to ErrInvalidSchema
	default:
		// unknown error, don't unwrap
		return e
	}
}
