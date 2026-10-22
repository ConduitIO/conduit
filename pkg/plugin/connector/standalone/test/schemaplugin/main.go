// Copyright © 2026 Meroxa, Inc.
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

// Package main is a standalone connector used by the connector utilities
// authentication tests. It is a real plugin process built on the connector SDK:
// when its source opens, it calls the Conduit connector utilities schema
// service with the token Conduit gave it, and writes the outcome to the file
// named by the "resultFile" setting.
package main

import (
	"context"
	"os"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit-connector-sdk/schema"
)

type sourceConfig struct {
	sdk.UnimplementedSourceConfig

	// ResultFile is where the outcome of the schema call is written.
	ResultFile string `json:"resultFile" validate:"required"`
}

type source struct {
	sdk.UnimplementedSource
	config sourceConfig
}

func newSource() sdk.Source { return &source{} }

func (s *source) Config() sdk.SourceConfig { return &s.config }

func (s *source) Open(ctx context.Context, _ opencdc.Position) error {
	_, err := schema.Create(ctx, schema.TypeAvro, "schemaplugin-subject",
		[]byte(`{"type":"record","name":"p","fields":[{"name":"id","type":"long"}]}`))
	result := "ok"
	if err != nil {
		result = "error: " + strings.ReplaceAll(err.Error(), "\n", " ")
	}
	// The result is the plugin's own report; the test reads it.
	return os.WriteFile(s.config.ResultFile, []byte(result), 0o600)
}

func (s *source) Read(ctx context.Context) (opencdc.Record, error) {
	<-ctx.Done()
	return opencdc.Record{}, ctx.Err()
}

func (s *source) Teardown(context.Context) error { return nil }

func main() {
	sdk.Serve(sdk.Connector{
		NewSpecification: func() sdk.Specification {
			return sdk.Specification{
				Name:    "schemaplugin",
				Summary: "test plugin that calls the connector utilities schema service",
				Version: "v0.0.1",
				Author:  "test",
				SourceParams: config.Parameters{
					"resultFile": {Type: config.ParameterTypeString, Description: "file the schema call outcome is written to", Validations: []config.Validation{config.ValidationRequired{}}},
				},
			}
		},
		NewSource: newSource,
	})
}
