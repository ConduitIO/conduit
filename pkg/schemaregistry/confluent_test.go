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

//go:build integration

package schemaregistry

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/stretchr/testify/assert"
)

const (
	connString      = "http://localhost:8085"
	healthCheckPath = "/health"
)

func TestNewConfluentService(t *testing.T) {
	ctx := context.Background()
	l := log.Nop()

	service := NewConfluentService(ctx, l, connString, healthCheckPath)

	assert.NotNil(t, service)
	assert.Equal(t, connString, service.connString)
	assert.Equal(t, healthCheckPath, service.healthCheckPath)
}

func TestConfluentService_Create(t *testing.T) {
	ctx := context.Background()
	l := log.Nop()

	service := NewConfluentService(ctx, l, connString, healthCheckPath)

	name := "test_schema"
	bytes := []byte(`{"type":"record","name":"test","fields":[{"name":"field1","type":"string"}]}`)

	instance, err := service.Create(ctx, name, bytes)
	assert.NoError(t, err)

	assert.Equal(t, bytes, instance.Bytes)
	assert.Equal(t, name, instance.Name)
}

func TestConfluentService_Get(t *testing.T) {
	ctx := context.Background()
	l := log.Nop()

	service := NewConfluentService(ctx, l, connString, healthCheckPath)

	expectedName := "test_schema"
	bytes := []byte(`{"type":"record","name":"test","fields":[{"name":"field1","type":"string"}]}`)

	instance, err := service.Create(ctx, expectedName, bytes)
	assert.NoError(t, err)

	gotInstance, err := service.Get(ctx, instance.ID)
	assert.NoError(t, err)
	assert.Equalf(t, gotInstance.ID, instance.ID, "expected id %s, got %s", instance.ID, gotInstance.ID)
	assert.Emptyf(t, gotInstance.Name, "expected empty name, got %s", gotInstance.Name)
	assert.Equalf(t, gotInstance.Bytes, instance.Bytes, "expected bytes %s, got %s", instance.Bytes, gotInstance.Bytes)
}

func TestConfluentService_Check(t *testing.T) {
	ctx := context.Background()
	l := log.Nop()

	service := NewConfluentService(ctx, l, connString, healthCheckPath)
	err := service.Check(ctx)

	assert.NoError(t, err)
}

func TestConfluentService_GetHealthCheckUrl(t *testing.T) {

	testCases := []struct {
		name            string
		connString      string
		healthCheckPath string
	}{
		{
			name:            "when connString has trailing slash and healthCheckPath has leading slash",
			connString:      "http://localhost:8085/",
			healthCheckPath: "/health",
		},
		{
			name:            "when connString has no trailing slash and healthCheckPath has no leading slash",
			connString:      "http://localhost:8085",
			healthCheckPath: "/health",
		},
		{
			name:            "when connString has no trailing slash and healthCheckPath has no leading slash",
			connString:      "http://localhost:8085",
			healthCheckPath: "health",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			l := log.Nop()

			service := NewConfluentService(ctx, l, tc.connString, tc.healthCheckPath)

			expectedURL := "http://localhost:8085/health"

			gotURL, err := service.getHealthCheckUrl()
			assert.NoError(t, err)
			assert.Equal(t, expectedURL, gotURL)
		})
	}

}
