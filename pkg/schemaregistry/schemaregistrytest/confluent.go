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
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ExampleSchemaRegistryURL points to the schema registry defined in
// /test/compose-schemaregistry.yaml.
// This method is only used if the tests are run with --tags=integration.
func ExampleSchemaRegistryURL(exampleName string, port int) (string, func()) {
	return "localhost:8085", func() {}
}

// TestSchemaRegistryURL points to the schema registries defined in
// /test/compose-schemaregistry.yaml.
// This method is only used if the tests are run with --tags=integration.
func TestSchemaRegistryURL(t testing.TB, authType string) string {
	t.Log("Using real schema registry server")
	switch authType {
	case "none":
		return "localhost:8085"
	case "basic":
		return "localhost:8086"
	default:
		return "localhost:8085"
	}
}

func WaitForSchemaRegistry(t testing.TB, url string, timeout time.Duration) error {
	url = "http://" + url
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", url+"/subjects", nil)
		req.SetBasicAuth("admin", "password")
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("schema registry did not become ready within %s", timeout)
}
