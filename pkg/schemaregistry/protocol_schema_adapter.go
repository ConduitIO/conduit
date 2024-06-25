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

	"github.com/conduitio/conduit-connector-protocol/conduit/pschema"
)

type protocolService struct {
	target Service
}

// NewProtocolServiceAdapter creates an adapter for Service that
// implements the pschema.Service interface from the protocol.
func NewProtocolServiceAdapter(s Service) pschema.Service {
	return &protocolService{target: s}
}

func (p *protocolService) Create(ctx context.Context, request pschema.CreateRequest) (pschema.CreateResponse, error) {
	res, err := p.target.Create(ctx, request.Subject, request.Bytes)
	if err != nil {
		return pschema.CreateResponse{}, err
	}

	return pschema.CreateResponse{
		Instance: res,
	}, nil
}

func (p *protocolService) Get(ctx context.Context, request pschema.GetRequest) (pschema.GetResponse, error) {
	res, err := p.target.Get(ctx, request.Subject, request.Version)
	if err != nil {
		return pschema.GetResponse{}, err
	}

	return pschema.GetResponse{
		Instance: res,
	}, nil
}
