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

//go:generate mockgen -destination=mock/schema_service.go -package=mock -mock_names=SchemaService=SchemaService . SchemaService

import (
	"context"
	conduitv1 "github.com/conduitio/conduit-connector-protocol/proto/conduit/v1"

	commschema "github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schema"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SchemaService interface {
	Create(ctx context.Context, schema commschema.Instance) (commschema.Instance, error)
	Get(ctx context.Context, id string) (commschema.Instance, error)
}

type SchemaServiceAPIv1 struct {
	conduitv1.UnimplementedSchemaServiceServer

	protoConv protoConverter
	service   SchemaService
}

func NewSchemaServiceAPIv1(s SchemaService) *SchemaServiceAPIv1 {
	return &SchemaServiceAPIv1{service: s}
}

func (s *SchemaServiceAPIv1) Register(ctx context.Context, req *conduitv1.CreateRequest) (*conduitv1.CreateResponse, error) {
	si, err := s.protoConv.schemaInstance(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to deserialize schema: %v", err)
	}

	sch, err := s.service.Create(ctx, si)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "registering failed: %v", err)
	}

	return s.protoConv.createResponse(sch), nil
}

func (s *SchemaServiceAPIv1) Fetch(ctx context.Context, req *conduitv1.GetRequest) (*conduitv1.GetResponse, error) {
	si, err := s.service.Get(ctx, req.Id)
	if cerrors.Is(err, schema.ErrSchemaNotFound) {
		return nil, status.Errorf(codes.NotFound, "schema with ID %v not found", req.Id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching schema %v failed: %v", req.Id, err)
	}

	return s.protoConv.getResponse(si), nil
}

// RegisterInServer registers the service in the server.
func (s *SchemaServiceAPIv1) RegisterInServer(srv *grpc.Server) {
	conduitv1.RegisterSchemaServiceServer(srv, s)
}
