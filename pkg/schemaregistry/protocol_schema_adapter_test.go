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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"testing"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit-connector-protocol/conduit/pschema"
	"github.com/conduitio/conduit/pkg/schemaregistry/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProtocolServiceAdapter_Create(t *testing.T) {
	testCases := []struct {
		name      string
		input     pschema.CreateRequest
		want      pschema.CreateResponse
		wantErr   error
		mockSetup func(*mock.Service)
	}{
		{
			name: "create",
			input: pschema.CreateRequest{
				Subject: "test-1",
				Type:    pschema.TypeAvro,
				Bytes:   []byte("123"),
			},
			want: pschema.CreateResponse{
				Instance: schema.Instance{
					Subject: "test-1",
					Version: 12,
					Type:    schema.TypeAvro,
					Bytes:   []byte("123"),
				},
			},
			wantErr: nil,
			mockSetup: func(m *mock.Service) {
				m.EXPECT().
					Create(gomock.Any(), "test-1", []byte("123")).
					Return(schema.Instance{
						Subject: "test-1",
						Version: 12,
						Type:    schema.TypeAvro,
						Bytes:   []byte("123"),
					}, nil)
			},
		},
		{
			name:    "create failure",
			input:   pschema.CreateRequest{},
			want:    pschema.CreateResponse{},
			wantErr: cerrors.New("boom"),
			mockSetup: func(m *mock.Service) {
				m.EXPECT().
					Create(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(schema.Instance{}, cerrors.New("boom"))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			pService := mock.NewService(gomock.NewController(t))
			tc.mockSetup(pService)

			underTest := NewProtocolServiceAdapter(pService)
			response, err := underTest.Create(ctx, tc.input)

			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(tc.want, response)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

func TestProtocolServiceAdapter_Get(t *testing.T) {
	testCases := []struct {
		name      string
		input     pschema.GetRequest
		want      pschema.GetResponse
		wantErr   error
		mockSetup func(*mock.Service)
	}{
		{
			name: "get",
			input: pschema.GetRequest{
				Subject: "test-1",
				Version: 12,
			},
			want: pschema.GetResponse{
				Instance: schema.Instance{
					Subject: "test-1",
					Version: 12,
					Type:    schema.TypeAvro,
					Bytes:   []byte("123"),
				},
			},
			wantErr: nil,
			mockSetup: func(m *mock.Service) {
				m.EXPECT().
					Get(gomock.Any(), "test-1", 12).
					Return(schema.Instance{
						Subject: "test-1",
						Version: 12,
						Type:    schema.TypeAvro,
						Bytes:   []byte("123"),
					}, nil)
			},
		},
		{
			name:    "create failure",
			input:   pschema.GetRequest{},
			want:    pschema.GetResponse{},
			wantErr: cerrors.New("boom"),
			mockSetup: func(m *mock.Service) {
				m.EXPECT().
					Get(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(schema.Instance{}, cerrors.New("boom"))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			pService := mock.NewService(gomock.NewController(t))
			tc.mockSetup(pService)

			underTest := NewProtocolServiceAdapter(pService)
			response, err := underTest.Get(ctx, tc.input)

			if tc.wantErr == nil {
				is.NoErr(err)
				is.Equal(tc.want, response)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}
