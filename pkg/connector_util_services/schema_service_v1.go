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

//go:generate mockgen -destination=mock/schema_service.go -package=mock -mock_names=SchemaService=SchemaService . SchemaService

import (
	"context"

	schemav1 "github.com/conduitio/conduit-connector-protocol/proto/schema/v1"
	"github.com/conduitio/conduit/pkg/schema"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SchemaService interface {
	Register(ctx context.Context, schema schema.Instance) (string, error)
	Fetch(ctx context.Context, id string) (schema.Instance, error)
}

type SchemaServiceAPIv1 struct {
	schemav1.UnimplementedSchemaServiceServer

	protoConv protoConverter
	service   SchemaService
}

func NewSchemaServiceAPIv1(s SchemaService) *SchemaServiceAPIv1 {
	return &SchemaServiceAPIv1{service: s}
}

func (s *SchemaServiceAPIv1) Register(ctx context.Context, req *schemav1.RegisterSchemaRequest) (*schemav1.RegisterSchemaResponse, error) {
	si, err := s.protoConv.schemaInstance(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to deserialize schema: %v", err)
	}

	id, err := s.service.Register(ctx, si)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "registering failed: %v", err)
	}

	return &schemav1.RegisterSchemaResponse{Id: id}, nil
}

func (s *SchemaServiceAPIv1) Fetch(ctx context.Context, req *schemav1.FetchSchemaRequest) (*schemav1.FetchSchemaResponse, error) {
	si, err := s.service.Fetch(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching schema %v failed: %v", req.Id, err)
	}

	return s.protoConv.fetchResponse(si), nil
}

// RegisterInServer registers the service in the server.
func (s *SchemaServiceAPIv1) RegisterInServer(srv *grpc.Server) {
	schemav1.RegisterSchemaServiceServer(srv, s)
}
