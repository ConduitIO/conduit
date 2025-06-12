// Copyright © 2025 Meroxa, Inc.
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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

const (
	SchemaRegistryUser     = "admin"
	SchemaRegistryPassword = "password"
)

func TestCreateSchema_BasicAuth(t *testing.T) {
	url := schemaregistrytest.TestSchemaRegistryURL(t, "basic")
	err := schemaregistrytest.WaitForSchemaRegistry(t, url, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	c, err := NewClient(
		logger,
		sr.URLs(url),
		sr.BasicAuth(SchemaRegistryUser, SchemaRegistryPassword),
	)
	is.NoErr(err)

	subject := "test-basic-auth"
	// create schema
	want := sr.Schema{
		Schema: `"string"`,
		Type:   sr.TypeAvro,
	}
	created, err := c.CreateSchema(ctx, subject, want)
	is.NoErr(err)

	// verify schema by subject/version
	got, err := c.SchemaBySubjectVersion(ctx, subject, created.Version)
	is.NoErr(err)
	is.Equal(created.ID, got.ID)
	is.Equal(subject, got.Subject)
}
