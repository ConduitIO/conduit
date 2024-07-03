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
	"context"

	"github.com/conduitio/conduit-connector-protocol/pconduit/v1/toproto"
	conduitv1 "github.com/conduitio/conduit-connector-protocol/proto/conduit/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SchemaServiceAPIv1 struct {
	conduitv1.UnimplementedSchemaServiceServer

	service schemaregistry.Service
}

func NewSchemaServiceAPIv1(s schemaregistry.Service) *SchemaServiceAPIv1 {
	return &SchemaServiceAPIv1{service: s}
}

func (s *SchemaServiceAPIv1) Create(ctx context.Context, req *conduitv1.CreateSchemaRequest) (*conduitv1.CreateSchemaResponse, error) {
	created, err := s.service.Create(ctx, req.Subject, req.Bytes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create schema: %v", err)
	}
	return toproto.CreateSchemaResponse(created)
}

func (s *SchemaServiceAPIv1) Get(ctx context.Context, req *conduitv1.GetSchemaRequest) (*conduitv1.GetSchemaResponse, error) {
	inst, err := s.service.Get(ctx, req.Subject, int(req.Version))
	if cerrors.Is(err, schemaregistry.ErrSchemaNotFound) {
		return nil, status.Errorf(
			codes.NotFound,
			"schema with name %v, version %v not found",
			req.Subject, req.Version,
		)
	}
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"getting schema with name %v, version %v failed: %v",
			req.Subject, req.Version, err,
		)
	}

	return toproto.GetSchemaResponse(inst)
}

// RegisterInServer registers the service in the server.
func (s *SchemaServiceAPIv1) RegisterInServer(srv *grpc.Server) {
	conduitv1.RegisterSchemaServiceServer(srv, s)
}
