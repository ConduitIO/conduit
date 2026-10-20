// Copyright © 2026 Meroxa, Inc.
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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/twmb/franz-go/pkg/sr"
)

// Decoder decodes Confluent-wire Protobuf payloads into structured data using
// the schema registry and a reference resolver.
type Decoder struct {
	registry schemaregistry.Registry
	resolver schema.Resolver
	logger   log.CtxLogger
}

func NewDecoder(registry schemaregistry.Registry, logger log.CtxLogger) *Decoder {
	return &Decoder{
		registry: registry,
		resolver: NewRegistryResolver(registry),
		logger:   logger.WithComponent("protobuf.internal.Decoder"),
	}
}

// Decode decodes b, a Confluent-wire Protobuf value: magic byte, schema ID,
// message-index prefix, then the Protobuf payload.
func (d *Decoder) Decode(ctx context.Context, b opencdc.RawData) (opencdc.StructuredData, error) {
	id, data, err := (&sr.ConfluentHeader{}).DecodeID(b.Bytes())
	if err != nil {
		return nil, cerrors.Errorf("failed to decode Confluent header: %w", err)
	}

	s, err := d.registry.SchemaByID(ctx, id)
	if err != nil {
		return nil, cerrors.Errorf("failed to get schema: %w", err)
	}

	sch := toschema.SrSchema(s)
	serde, err := sch.SerdeWithResolver(ctx, d.resolver)
	if err != nil {
		return nil, cerrors.Errorf("failed to compile schema (ID: %v): %w", id, err)
	}

	var out opencdc.StructuredData
	if err := serde.Unmarshal(data, &out); err != nil {
		return nil, cerrors.Errorf("failed to unmarshal data with schema (ID: %v): %w", id, err)
	}

	return out, nil
}
