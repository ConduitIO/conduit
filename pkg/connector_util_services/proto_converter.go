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

package connector_util_services

import (
	"fmt"

	schemav1 "github.com/conduitio/conduit-connector-protocol/proto/schema_service/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schema"
)

type protoConverter struct {
}

func (c protoConverter) schemaInstance(req *schemav1.RegisterSchemaRequest) (schema.Instance, error) {
	typ, err := c.schemaType(req.Type)
	if err != nil {
		return schema.Instance{}, fmt.Errorf("invalid schema type: %w", err)
	}

	return schema.Instance{
		Name:    req.Name,
		Version: req.Version,
		Type:    typ,
		Bytes:   req.Bytes,
	}, nil
}

func (c protoConverter) schemaType(typ schemav1.SchemaType) (schema.Type, error) {
	switch typ {
	case schemav1.SchemaType_TYPE_AVRO:
		return schema.TypeAvro, nil
	default:
		return 0, cerrors.Errorf("unsupported %q", typ)
	}
}

func (c protoConverter) fetchResponse(inst schema.Instance) *schemav1.FetchSchemaResponse {
	return &schemav1.FetchSchemaResponse{
		Id:      inst.ID,
		Name:    inst.Name,
		Version: inst.Version,
		Type:    c.protoType(inst.Type),
		Bytes:   inst.Bytes,
	}
}

func (c protoConverter) protoType(t schema.Type) schemav1.SchemaType {
	switch t {
	case schema.TypeAvro:
		return schemav1.SchemaType_TYPE_AVRO
	default:
		panic(fmt.Errorf("unsupported schema type %q", t))
	}
}
