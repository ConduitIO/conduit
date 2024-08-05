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

//go:build !integration

package schemaregistrytest

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/neilotoole/slogt"
)

var (
	serverByTest     = make(map[string]*httptest.Server)
	serverByTestLock sync.Mutex
)

// ExampleSchemaRegistryURL creates a fake in-memory schema registry server and
// returns its address and a cleanup function which should be executed in a
// deferred call.
//
// This method is only used if examples are run without --tags=integration. It
// is meant as a utility to allow faster iteration when developing, please run
// integration tests to ensure the code works with a real schema registry.
func ExampleSchemaRegistryURL(exampleName string, port int) (string, func()) {
	// Discard all schema registry logs in examples.
	// Related proposal: https://github.com/golang/go/issues/62005
	// Until the proposal goes through, we need to discard logs through the text handler.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return inMemorySchemaRegistryURL(exampleName, logger, port)
}

// TestSchemaRegistryURL creates a fake in-memory schema registry server and
// returns its address.
//
// This method is only used if the tests are run without
// --tags=integration. It is meant as a utility to allow faster iteration when
// developing, please run integration tests to ensure the code works with a real
// schema registry.
func TestSchemaRegistryURL(t testing.TB) string {
	url, cleanup := inMemorySchemaRegistryURL(t.Name(), slogt.New(t), 0)
	t.Cleanup(cleanup)
	return url
}

func TestSchemaRegistry() (*schemaregistry.SchemaRegistry, error) {
	return schemaregistry.NewSchemaRegistry(&inmemory.DB{})
}

func inMemorySchemaRegistryURL(name string, logger *slog.Logger, port int) (string, func()) {
	serverByTestLock.Lock()
	defer serverByTestLock.Unlock()

	srv := serverByTest[name]
	cleanup := func() {}
	if srv == nil {
		mux := http.NewServeMux()
		schemaRegistry, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
		if err != nil {
			panic(fmt.Sprintf("failed creating schema registry: %v", err))
		}
		schemaSrv := schemaregistry.NewServer(logger, schemaRegistry)
		schemaSrv.RegisterHandlers(mux)
		srv = httptest.NewUnstartedServer(mux)
		if port > 0 {
			// NewUnstartedServer creates a listener. Close that listener and replace
			// with a custom one.
			_ = srv.Listener.Close()
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				panic(fmt.Sprintf("failed starting test server on port %d: %v", port, err))
			}
			srv.Listener = l
		}

		srv.Start()
		serverByTest[name] = srv
		cleanup = srv.Close
	}
	return srv.URL, cleanup
}
