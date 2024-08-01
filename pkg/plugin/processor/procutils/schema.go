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

package procutils

import (
	"context"

	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	conduitschemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/fromschema"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/twmb/franz-go/pkg/sr"
)

type SchemaService struct {
	registry schemaregistry.Registry
	logger   log.CtxLogger
}

var _ pprocutils.SchemaService = (*SchemaService)(nil)

func NewSchemaService(logger log.CtxLogger, registry schemaregistry.Registry) *SchemaService {
	return &SchemaService{
		registry: registry,
		logger:   logger.WithComponent("procutils.SchemaService"),
	}
}

func (s *SchemaService) Check(ctx context.Context) error {
	r, ok := s.registry.(schemaregistry.RegistryWithCheck)
	if !ok {
		return nil
	}
	return r.Check(ctx)
}

func (s *SchemaService) CreateSchema(ctx context.Context, req pprocutils.CreateSchemaRequest) (pprocutils.CreateSchemaResponse, error) {
	ss, err := s.registry.CreateSchema(ctx, req.Subject, sr.Schema{
		Schema: string(req.Bytes),
		Type:   fromschema.SrSchemaType(req.Type),
	})
	if err != nil {
		var respErr *sr.ResponseError
		if cerrors.As(err, &respErr) {
			return pprocutils.CreateSchemaResponse{}, unwrapSrError(respErr)
		}
		return pprocutils.CreateSchemaResponse{}, cerrors.Errorf("failed to create schema: %w", err)
	}
	return pprocutils.CreateSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}

func (s *SchemaService) GetSchema(ctx context.Context, req pprocutils.GetSchemaRequest) (pprocutils.GetSchemaResponse, error) {
	ss, err := s.registry.SchemaBySubjectVersion(ctx, req.Subject, req.Version)
	if err != nil {
		var respErr *sr.ResponseError
		if cerrors.As(err, &respErr) {
			return pprocutils.GetSchemaResponse{}, unwrapSrError(respErr)
		}
		return pprocutils.GetSchemaResponse{}, cerrors.Errorf("failed to get schema by subject and version: %w", err)
	}

	return pprocutils.GetSchemaResponse{
		Schema: toschema.SrSubjectSchema(ss),
	}, nil
}

func unwrapSrError(e *sr.ResponseError) error {
	switch e.ErrorCode {
	case conduitschemaregistry.ErrorCodeSubjectNotFound:
		return pprocutils.ErrSubjectNotFound
	case conduitschemaregistry.ErrorCodeVersionNotFound:
		return pprocutils.ErrVersionNotFound
	case conduitschemaregistry.ErrorCodeInvalidSchema:
		return pprocutils.ErrInvalidSchema
	default:
		return e
	}
}
