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

package connutils

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconduit"
	conduitschemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestSchemaService_CreateSchema_ValidateToken(t *testing.T) {
	testCases := []struct {
		name    string
		token   string
		wantErr string
	}{
		{
			name:    "no token",
			token:   "",
			wantErr: "\"\": invalid token",
		},
		{
			name:    "invalid token",
			token:   "abc",
			wantErr: "\"abc\": invalid token",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := pconduit.ContextWithConnectorToken(context.Background(), tc.token)

			underTest := NewSchemaService(
				log.Nop(),
				conduitschemaregistry.NewSchemaRegistry(),
				NewTokenService(),
			)
			_, err := underTest.CreateSchema(ctx, pconduit.CreateSchemaRequest{})

			is.True(err != nil)
			is.Equal(err.Error(), tc.wantErr)
		})
	}
}

func TestSchemaService_GetSchema_ValidateToken(t *testing.T) {
	testCases := []struct {
		name    string
		token   string
		wantErr string
	}{
		{
			name:    "no token",
			token:   "",
			wantErr: "\"\": invalid token",
		},
		{
			name:    "invalid token",
			token:   "abc",
			wantErr: "\"abc\": invalid token",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := pconduit.ContextWithConnectorToken(context.Background(), tc.token)

			underTest := NewSchemaService(
				log.Nop(),
				conduitschemaregistry.NewSchemaRegistry(),
				NewTokenService(),
			)
			_, err := underTest.GetSchema(ctx, pconduit.GetSchemaRequest{})

			is.True(err != nil)
			is.Equal(err.Error(), tc.wantErr)
		})
	}
}
