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
	"testing"

	schemav1 "github.com/conduitio/conduit-commons/proto/schema/v1"
	"github.com/conduitio/conduit-commons/schema"
	conduitv1 "github.com/conduitio/conduit-connector-protocol/proto/conduit/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schemaregistry/mock"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSchemaService_Create(t *testing.T) {
	testCases := []struct {
		name         string
		input        *conduitv1.CreateSchemaRequest
		setupService func(*mock.Service, *conduitv1.CreateSchemaRequest)
		wantResponse *conduitv1.CreateSchemaResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &conduitv1.CreateSchemaRequest{
				Subject: "my-collection",
				Type:    schemav1.Schema_TYPE_AVRO,
				Bytes:   []byte{1, 2, 3},
			},
			setupService: func(svc *mock.Service, req *conduitv1.CreateSchemaRequest) {
				svc.EXPECT().
					Create(gomock.Any(), req.Subject, req.Bytes).
					Return(
						schema.Schema{
							Subject: "my-collection",
							Version: 0,
							Type:    schema.TypeAvro,
							Bytes:   []byte{1, 2, 3},
						},
						nil,
					)
			},
			wantResponse: &conduitv1.CreateSchemaResponse{
				Schema: &schemav1.Schema{
					Subject: "my-collection",
					Version: 0,
					Type:    schemav1.Schema_TYPE_AVRO,
					Bytes:   []byte{1, 2, 3},
				},
			},
		},
		{
			name: "service error",
			input: &conduitv1.CreateSchemaRequest{
				Subject: "my-collection",
				Type:    schemav1.Schema_TYPE_AVRO,
				Bytes:   []byte{1, 2, 3},
			},
			setupService: func(svc *mock.Service, req *conduitv1.CreateSchemaRequest) {
				svc.EXPECT().
					Create(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(schema.Schema{}, cerrors.New("boom"))
			},
			wantErr: status.Error(codes.Internal, `failed to create schema: boom`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			svc := mock.NewService(gomock.NewController(t))
			if tc.setupService != nil {
				tc.setupService(svc, tc.input)
			}
			underTest := NewSchemaServiceAPIv1(svc)

			gotResponse, err := underTest.Create(ctx, tc.input)
			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(
					"",
					cmp.Diff(
						tc.wantResponse,
						gotResponse,
						cmpopts.IgnoreUnexported(conduitv1.CreateSchemaResponse{}, schemav1.Schema{}),
					),
				)
			} else {
				is.True(err != nil) // expected an error
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

func TestSchemaService_Get(t *testing.T) {
	testCases := []struct {
		name         string
		input        *conduitv1.GetSchemaRequest
		setupService func(*mock.Service, *conduitv1.GetSchemaRequest)
		wantResponse *conduitv1.GetSchemaResponse
		wantErr      error
	}{
		{
			name: "valid request",
			input: &conduitv1.GetSchemaRequest{
				Subject: "abc",
				Version: 123,
			},
			setupService: func(svc *mock.Service, req *conduitv1.GetSchemaRequest) {
				svc.EXPECT().
					Get(gomock.Any(), "abc", 123).
					Return(schema.Schema{
						Subject: "my-collection",
						Version: 321,
						Type:    schema.TypeAvro,
						Bytes:   []byte{1, 2, 3},
					}, nil)
			},
			wantResponse: &conduitv1.GetSchemaResponse{
				Schema: &schemav1.Schema{
					Subject: "my-collection",
					Version: 321,
					Type:    schemav1.Schema_TYPE_AVRO,
					Bytes:   []byte{1, 2, 3},
				},
			},
		},
		{
			name: "service error",
			input: &conduitv1.GetSchemaRequest{
				Subject: "abc",
				Version: 123,
			},
			setupService: func(svc *mock.Service, req *conduitv1.GetSchemaRequest) {
				svc.EXPECT().
					Get(gomock.Any(), "abc", 123).
					Return(schema.Schema{}, cerrors.New("boom"))
			},
			wantErr: status.Error(codes.Internal, "getting schema with name abc, version 123 failed: boom"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			svc := mock.NewService(gomock.NewController(t))
			if tc.setupService != nil {
				tc.setupService(svc, tc.input)
			}
			underTest := NewSchemaServiceAPIv1(svc)

			gotResponse, err := underTest.Get(ctx, tc.input)
			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(
					"",
					cmp.Diff(
						tc.wantResponse,
						gotResponse,
						cmpopts.IgnoreUnexported(conduitv1.GetSchemaResponse{}, schemav1.Schema{}),
					),
				)
			} else {
				is.True(err != nil) // expected an error
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}
