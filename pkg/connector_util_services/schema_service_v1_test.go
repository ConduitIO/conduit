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
	"context"
	schemav1 "github.com/conduitio/conduit-connector-protocol/proto/schema_service/v1"
	"github.com/conduitio/conduit/pkg/connector_util_services/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schema"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestSchemaService_Register(t *testing.T) {
	testCases := []struct {
		name         string
		input        *schemav1.RegisterSchemaRequest
		setupService func(*mock.SchemaService, *schemav1.RegisterSchemaRequest)
		wantResponse *schemav1.RegisterSchemaResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &schemav1.RegisterSchemaRequest{
				Name:    "my-collection",
				Version: 0,
				Type:    schemav1.SchemaType_TYPE_AVRO,
				Bytes:   []byte{1, 2, 3},
			},
			setupService: func(svc *mock.SchemaService, req *schemav1.RegisterSchemaRequest) {
				svc.EXPECT().
					Register(gomock.Any(), schema.Instance{
						Name:    "my-collection",
						Version: 0,
						Type:    schema.TypeAvro,
						Bytes:   []byte{1, 2, 3},
					}).
					Return("abc", nil)
			},
			wantResponse: &schemav1.RegisterSchemaResponse{
				Id: "abc",
			},
		},
		{
			name: "unknown schema type",
			input: &schemav1.RegisterSchemaRequest{
				Name:    "my-collection",
				Version: 0,
				Type:    schemav1.SchemaType_TYPE_UNDEFINED,
				Bytes:   []byte{1, 2, 3},
			},
			wantErr: status.Error(codes.InvalidArgument, `failed to deserialize schema: invalid schema type: unsupported "TYPE_UNDEFINED"`),
		},
		{
			name: "service error",
			input: &schemav1.RegisterSchemaRequest{
				Name:    "my-collection",
				Version: 0,
				Type:    schemav1.SchemaType_TYPE_AVRO,
				Bytes:   []byte{1, 2, 3},
			},
			setupService: func(svc *mock.SchemaService, req *schemav1.RegisterSchemaRequest) {
				svc.EXPECT().
					Register(gomock.Any(), schema.Instance{
						Name:    "my-collection",
						Version: 0,
						Type:    schema.TypeAvro,
						Bytes:   []byte{1, 2, 3},
					}).
					Return("", cerrors.New("boom"))
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
					cmp.Diff(tc.wantResponse, gotResponse, cmpopts.IgnoreUnexported(schemav1.RegisterSchemaResponse{})),
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
		input        *schemav1.FetchSchemaRequest
		setupService func(*mock.SchemaService, *schemav1.FetchSchemaRequest)
		wantResponse *schemav1.FetchSchemaResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &schemav1.FetchSchemaRequest{
				Id: "abc",
			},
			setupService: func(svc *mock.SchemaService, req *schemav1.FetchSchemaRequest) {
				svc.EXPECT().
					Fetch(gomock.Any(), "abc").
					Return(schema.Instance{
						ID:      "abc",
						Name:    "my-collection",
						Version: 321,
						Type:    schema.TypeAvro,
						Bytes:   []byte{1, 2, 3},
					}, nil)
			},
			wantResponse: &schemav1.FetchSchemaResponse{
				Id:      "abc",
				Name:    "my-collection",
				Version: 321,
				Type:    schemav1.SchemaType_TYPE_AVRO,
				Bytes:   []byte{1, 2, 3},
			},
		},
		{
			name: "service error",
			input: &schemav1.FetchSchemaRequest{
				Id: "abc",
			},
			setupService: func(svc *mock.SchemaService, req *schemav1.FetchSchemaRequest) {
				svc.EXPECT().
					Fetch(gomock.Any(), "abc").
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
					cmp.Diff(tc.wantResponse, gotResponse, cmpopts.IgnoreUnexported(schemav1.FetchSchemaResponse{})),
				)
			} else {
				is.True(err != nil) // expected an error
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}
