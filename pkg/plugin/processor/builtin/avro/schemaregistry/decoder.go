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

type Decoder struct {
	client *Client
	serde  *sr.Serde
	logger log.CtxLogger
}

func NewDecoder(client *Client, logger log.CtxLogger, serde *sr.Serde) *Decoder {
	return &Decoder{
		client: client,
		serde:  serde,
		logger: logger.WithComponent("schemaregistry.Decoder"),
	}
}

func (d *Decoder) Decode(ctx context.Context, b record.RawData) (record.StructuredData, error) {
	var out record.StructuredData
	err := d.serde.Decode(b.Raw, &out)
	if cerrors.Is(err, sr.ErrNotRegistered) {
		err = d.findAndRegisterSchema(ctx, b)
		if err != nil {
			return nil, err
		}
		// retry decoding
		err = d.serde.Decode(b.Raw, &out)
	}
	if err != nil {
		return nil, cerrors.Errorf("failed to decode raw data: %w", err)
	}

	return out, nil
}

func (d *Decoder) findAndRegisterSchema(ctx context.Context, b record.RawData) error {
	id, _, _ := d.serde.Header().DecodeID(b.Raw) // we know this won't throw an error since Decode didn't return ErrBadHeader
	s, err := d.client.SchemaByID(ctx, id)
	if err != nil {
		return cerrors.Errorf("failed to get schema: %w", err)
	}
	sf, ok := DefaultSchemaFactories[s.Type]
	if !ok {
		return cerrors.Errorf("unknown schema type %q (%d)", s.Type.String(), s.Type)
	}
	schema, err := sf.Parse(s.Schema)
	if err != nil {
		return cerrors.Errorf("failed to parse schema: %w", err)
	}

	d.serde.Register(
		id,
		record.StructuredData{},
		sr.EncodeFn(encodeFn(schema, sr.SubjectSchema{ID: id})),
		sr.DecodeFn(decodeFn(schema, sr.SubjectSchema{ID: id})),
	)
	return nil
}

func decodeFn(schema Schema, ss sr.SubjectSchema) func(b []byte, a any) error {
	return func(b []byte, a any) error {
		err := schema.Unmarshal(b, a)
		if err != nil {
			return cerrors.Errorf("failed to unmarshal data with schema (ID: %v, subject: %v, version: %v): %w", ss.ID, ss.Subject, ss.Version, err)
		}
		return nil
	}
}
