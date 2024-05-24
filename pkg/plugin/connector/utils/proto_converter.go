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

package utils

import (
	"fmt"
	conduitv1 "github.com/conduitio/conduit-connector-protocol/proto/conduit/v1"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

type protoConverter struct {
}

func (c protoConverter) schemaInstance(req *conduitv1.CreateRequest) (schema.Instance, error) {
	typ, err := c.schemaType(req.Type)
	if err != nil {
		return schema.Instance{}, fmt.Errorf("invalid schema type: %w", err)
	}

	return schema.Instance{
		Name:  req.Name,
		Type:  typ,
		Bytes: req.Bytes,
	}, nil
}

func (c protoConverter) schemaType(typ conduitv1.Schema_Type) (schema.Type, error) {
	switch typ {
	case conduitv1.Schema_TYPE_AVRO:
		return schema.TypeAvro, nil
	default:
		return 0, cerrors.Errorf("unsupported %q", typ)
	}
}

func (c protoConverter) getResponse(inst schema.Instance) *conduitv1.GetResponse {
	return &conduitv1.GetResponse{
		Schema: &conduitv1.Schema{
			Id:      inst.ID,
			Name:    inst.Name,
			Version: inst.Version,
			Type:    c.protoType(inst.Type),
			Bytes:   inst.Bytes,
		},
	}
}

func (c protoConverter) protoType(t schema.Type) conduitv1.Schema_Type {
	switch t {
	case schema.TypeAvro:
		return conduitv1.Schema_TYPE_AVRO
	default:
		panic(fmt.Errorf("unsupported schema type %q", t))
	}
}

func (c protoConverter) createResponse(inst schema.Instance) *conduitv1.CreateResponse {
	return &conduitv1.CreateResponse{
		Schema: &conduitv1.Schema{
			Id:      inst.ID,
			Name:    inst.Name,
			Version: inst.Version,
			Type:    c.protoType(inst.Type),
			Bytes:   inst.Bytes,
		},
	}
}
