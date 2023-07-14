// Copyright © 2023 Meroxa, Inc.
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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type Encoder struct {
	client *Client
	serde  *sr.Serde
	logger log.CtxLogger

	SchemaStrategy
}

type SchemaStrategy interface {
	GetSchema(context.Context, *Client, log.CtxLogger, record.StructuredData) (Schema, sr.SubjectSchema, error)
}

func NewEncoder(client *Client, logger log.CtxLogger, serde *sr.Serde, strategy SchemaStrategy) *Encoder {
	return &Encoder{
		client:         client,
		serde:          serde,
		logger:         logger.WithComponent("schemaregistry.Encoder"),
		SchemaStrategy: strategy,
	}
}

func (e *Encoder) Encode(ctx context.Context, sd record.StructuredData) (record.RawData, error) {
	s, ss, err := e.GetSchema(ctx, e.client, e.logger, sd)
	if err != nil {
		return record.RawData{}, cerrors.Errorf("failed to get schema: %w", err)
	}

	b, err := e.serde.Encode(sd, sr.ID(ss.ID))
	if cerrors.Is(err, sr.ErrNotRegistered) {
		// TODO note that we need to register specific indexes when adding support for protobuf
		e.serde.Register(
			ss.ID,
			record.StructuredData{},
			sr.EncodeFn(encodeFn(s, ss)),
			sr.DecodeFn(decodeFn(s, ss)),
		)

		// try to encode again
		b, err = e.serde.Encode(sd, sr.ID(ss.ID))
	}
	if err != nil {
		return record.RawData{}, cerrors.Errorf("failed to encode data: %w", err)
	}
	return record.RawData{Raw: b}, nil
}

type ExtractAndUploadSchemaStrategy struct {
	Type    sr.SchemaType
	Subject string
}

func (str ExtractAndUploadSchemaStrategy) GetSchema(ctx context.Context, client *Client, _ log.CtxLogger, sd record.StructuredData) (Schema, sr.SubjectSchema, error) {
	sf, ok := DefaultSchemaFactories[str.Type]
	if !ok {
		return nil, sr.SubjectSchema{}, cerrors.Errorf("unknown schema type %q (%d)", str.Type.String(), str.Type)
	}

	s, err := sf.SchemaForType(sd)
	if err != nil {
		return nil, sr.SubjectSchema{}, cerrors.Errorf("could not extract avro schema: %w", err)
	}

	ss, err := client.CreateSchema(ctx, str.Subject, sr.Schema{
		Schema:     s.String(),
		Type:       str.Type,
		References: nil,
	})
	if err != nil {
		return nil, sr.SubjectSchema{}, cerrors.Errorf("could not create schema: %w", err)
	}

	return s, ss, nil
}

type DownloadSchemaStrategy struct {
	Subject string
	// TODO add support for specifying "latest" - https://github.com/ConduitIO/conduit/issues/1095
	Version int
}

func (str DownloadSchemaStrategy) GetSchema(ctx context.Context, client *Client, _ log.CtxLogger, _ record.StructuredData) (Schema, sr.SubjectSchema, error) {
	// fetch schema from registry
	ss, err := client.SchemaBySubjectVersion(ctx, str.Subject, str.Version)
	if err != nil {
		return nil, sr.SubjectSchema{}, cerrors.Errorf("could not fetch schema with subject %q and version %q: %w", str.Subject, str.Version, err)
	}

	sf, ok := DefaultSchemaFactories[ss.Type]
	if !ok {
		return nil, sr.SubjectSchema{}, cerrors.Errorf("unknown schema type %q (%d)", ss.Type.String(), ss.Type)
	}

	s, err := sf.Parse(ss.Schema.Schema)
	if err != nil {
		return nil, sr.SubjectSchema{}, err
	}
	return s, ss, nil
}

func encodeFn(schema Schema, ss sr.SubjectSchema) func(v any) ([]byte, error) {
	return func(v any) ([]byte, error) {
		b, err := schema.Marshal(v)
		if err != nil {
			return nil, cerrors.Errorf("failed to marshal data with schema (ID: %v, subject: %v, version: %v): %w", ss.ID, ss.Subject, ss.Version, err)
		}
		return b, nil
	}
}
