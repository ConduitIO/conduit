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
	conduitv1 "github.com/conduitio/conduit-connector-protocol/proto/conduit/v1"
	"testing"

	schema "github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector/utils/mock"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSchemaService_Register(t *testing.T) {
	testCases := []struct {
		name         string
		input        *conduitv1.CreateRequest
		setupService func(*mock.SchemaService, *conduitv1.CreateRequest)
		wantResponse *conduitv1.CreateResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &conduitv1.CreateRequest{
				Name:  "my-collection",
				Type:  conduitv1.Schema_TYPE_AVRO,
				Bytes: []byte{1, 2, 3},
			},
			setupService: func(svc *mock.SchemaService, req *conduitv1.CreateRequest) {
				svc.EXPECT().
					Create(
						gomock.Any(),
						schema.Instance{
							Name:    "my-collection",
							Version: 0,
							Type:    schema.TypeAvro,
							Bytes:   []byte{1, 2, 3},
						},
					).
					Return(
						schema.Instance{
							ID:      "123",
							Name:    "my-collection",
							Version: 0,
							Type:    schema.TypeAvro,
							Bytes:   []byte{1, 2, 3},
						},
						nil,
					)
			},
			wantResponse: &conduitv1.CreateResponse{
				Schema: &conduitv1.Schema{
					Id:      "123",
					Name:    "my-collection",
					Version: 0,
					Type:    conduitv1.Schema_TYPE_AVRO,
					Bytes:   []byte{1, 2, 3},
				},
			},
		},
		{
			name: "unknown schema type",
			input: &conduitv1.CreateRequest{
				Name:  "my-collection",
				Type:  conduitv1.Schema_TYPE_UNSPECIFIED,
				Bytes: []byte{1, 2, 3},
			},
			wantErr: status.Error(codes.InvalidArgument, `failed to deserialize schema: invalid schema type: unsupported "TYPE_UNSPECIFIED"`),
		},
		{
			name: "service error",
			input: &conduitv1.CreateRequest{
				Name:  "my-collection",
				Type:  conduitv1.Schema_TYPE_AVRO,
				Bytes: []byte{1, 2, 3},
			},
			setupService: func(svc *mock.SchemaService, req *conduitv1.CreateRequest) {
				svc.EXPECT().
					Create(gomock.Any(), gomock.Any()).
					Return(schema.Instance{}, cerrors.New("boom"))
			},
			wantErr: status.Error(codes.Internal, `registering failed: boom`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			svc := mock.NewSchemaService(gomock.NewController(t))
			if tc.setupService != nil {
				tc.setupService(svc, tc.input)
			}
			underTest := NewSchemaServiceAPIv1(svc)

			gotResponse, err := underTest.Register(ctx, tc.input)
			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(
					"",
					cmp.Diff(
						tc.wantResponse,
						gotResponse,
						cmpopts.IgnoreUnexported(conduitv1.CreateResponse{}, conduitv1.Schema{}),
					),
				)
			} else {
				is.True(err != nil) // expected an error
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

func TestSchemaService_Fetch(t *testing.T) {
	testCases := []struct {
		name         string
		input        *conduitv1.GetRequest
		setupService func(*mock.SchemaService, *conduitv1.GetRequest)
		wantResponse *conduitv1.GetResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &conduitv1.GetRequest{
				Id: "abc",
			},
			setupService: func(svc *mock.SchemaService, req *conduitv1.GetRequest) {
				svc.EXPECT().
					Get(gomock.Any(), "abc").
					Return(schema.Instance{
						ID:      "abc",
						Name:    "my-collection",
						Version: 321,
						Type:    schema.TypeAvro,
						Bytes:   []byte{1, 2, 3},
					}, nil)
			},
			wantResponse: &conduitv1.GetResponse{
				Schema: &conduitv1.Schema{
					Id:      "abc",
					Name:    "my-collection",
					Version: 321,
					Type:    conduitv1.Schema_TYPE_AVRO,
					Bytes:   []byte{1, 2, 3},
				},
			},
		},
		{
			name: "service error",
			input: &conduitv1.GetRequest{
				Id: "abc",
			},
			setupService: func(svc *mock.SchemaService, req *conduitv1.GetRequest) {
				svc.EXPECT().
					Get(gomock.Any(), "abc").
					Return(schema.Instance{}, cerrors.New("boom"))
			},
			wantErr: status.Error(codes.Internal, "fetching schema abc failed: boom"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			svc := mock.NewSchemaService(gomock.NewController(t))
			if tc.setupService != nil {
				tc.setupService(svc, tc.input)
			}
			underTest := NewSchemaServiceAPIv1(svc)

			gotResponse, err := underTest.Fetch(ctx, tc.input)
			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(
					"",
					cmp.Diff(
						tc.wantResponse,
						gotResponse,
						cmpopts.IgnoreUnexported(conduitv1.GetResponse{}, conduitv1.Schema{}),
					),
				)
			} else {
				is.True(err != nil) // expected an error
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}
