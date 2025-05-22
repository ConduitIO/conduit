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

package schemaregistrytest

import (
	"testing"
)

// ExampleSchemaRegistryURL points to the schema registry defined in
// /test/compose-schemaregistry.yaml.
// This method is only used if the tests are run with --tags=integration.
func ExampleSchemaRegistryURL(exampleName string, port int) (string, func()) {
	return "localhost:8085", func() {}
}

// TestSchemaRegistryURL points to the schema registry defined in
// /test/compose-schemaregistry.yaml.
// This method is only used if the tests are run with --tags=integration.
func TestSchemaRegistryURL(t testing.TB) string {
	t.Log("Using real schema registry server")
	return "localhost:8085"
}
