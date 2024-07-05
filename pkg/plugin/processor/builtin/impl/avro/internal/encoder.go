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

package internal

import (
	"context"

	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"

	"github.com/conduitio/conduit/pkg/schemaregistry"

	"github.com/conduitio/conduit-commons/schema"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/twmb/franz-go/pkg/sr"
)

type Encoder struct {
	registry schemaregistry.Registry
	logger   log.CtxLogger

	SchemaStrategy
}

type SchemaStrategy interface {
	GetSchema(context.Context, schemaregistry.Registry, log.CtxLogger, opencdc.StructuredData) (schema.Schema, error)
}

func NewEncoder(registry schemaregistry.Registry, logger log.CtxLogger, strategy SchemaStrategy) *Encoder {
	return &Encoder{
		registry:       registry,
		logger:         logger.WithComponent("avro.internal.Encoder"),
		SchemaStrategy: strategy,
	}
}

func (e *Encoder) Encode(ctx context.Context, sd opencdc.StructuredData) (opencdc.RawData, error) {
	s, err := e.GetSchema(ctx, e.registry, e.logger, sd)
	if err != nil {
		return opencdc.RawData{}, cerrors.Errorf("failed to get schema: %w", err)
	}

	// TODO note that we need to pass in the index when adding support for protobuf
	header, err := (&sr.ConfluentHeader{}).AppendEncode(nil, s.ID, nil)
	if err != nil {
		return nil, cerrors.Errorf("failed to encode header: %w", err)
	}

	data, err := s.Marshal(sd)
	if err != nil {
		return nil, cerrors.Errorf("failed to marshal data with schema (ID: %v, subject: %v, version: %v): %w", s.ID, s.Subject, s.Version, err)
	}

	return append(header, data...), nil
}

type ExtractAndUploadSchemaStrategy struct {
	Subject string
}

func (str ExtractAndUploadSchemaStrategy) GetSchema(ctx context.Context, registry schemaregistry.Registry, _ log.CtxLogger, sd opencdc.StructuredData) (schema.Schema, error) {
	s, err := schema.KnownSerdeFactories[schema.TypeAvro].SerdeForType(sd)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not extract avro schema: %w", err)
	}

	ss, err := registry.CreateSchema(ctx, str.Subject, sr.Schema{
		Schema: s.String(),
		Type:   sr.TypeAvro,
	})
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not create schema: %w", err)
	}

	return toschema.SrSubjectSchema(ss), nil
}

type DownloadSchemaStrategy struct {
	Subject string
	// TODO add support for specifying "latest" - https://github.com/ConduitIO/conduit/issues/1095
	Version int
}

func (str DownloadSchemaStrategy) GetSchema(ctx context.Context, registry schemaregistry.Registry, _ log.CtxLogger, _ opencdc.StructuredData) (schema.Schema, error) {
	// get schema from registry
	ss, err := registry.SchemaBySubjectVersion(ctx, str.Subject, str.Version)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not get schema with subject %q and version %q: %w", str.Subject, str.Version, err)
	}

	return toschema.SrSubjectSchema(ss), nil
}
