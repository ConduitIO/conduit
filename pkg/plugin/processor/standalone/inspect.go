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

package standalone

import (
	"context"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// InspectSpecification compiles the WASM module at path with the SAME wazero
// runtime and Conduit host module the standalone processor registry uses at
// load time (it reuses loadBlueprint verbatim), instantiates it once with
// deny-all egress and a throwaway in-memory schema service, and returns its
// self-declared Specification.
//
// It is the install-time validation primitive pkg/registry uses to confirm a
// fetched processor artifact (a) compiles under wazero at all and (b) declares
// the name the runtime will actually register it under — closing the gap
// between the registry index name and Specification().Name, which is what
// NewRegistry keys a discovered plugin on (see registry.go's loadBlueprint),
// NOT the on-disk filename.
//
// It builds and tears down its OWN throwaway runtime; it never scans a
// directory and never registers anything into a live Registry. Because it
// reuses loadBlueprint, install-time validation can never diverge from runtime
// discovery: if this returns a spec for a file, a NewRegistry over that same
// file will register exactly that spec.
//
// Concurrency: safe to call concurrently — each call owns an independent
// runtime (a wazero.Runtime is not itself goroutine-safe, but nothing here is
// shared across calls).
//
// This lives here, in the standalone package, rather than in pkg/registry, so
// there is a single WASM loader: pkg/registry importing this creates no import
// cycle (standalone does not import pkg/registry).
func InspectSpecification(ctx context.Context, logger log.CtxLogger, path string) (sdk.Specification, error) {
	logger = logger.WithComponentFromType(Registry{})

	// Use the package-level newRuntime hook so this honors the interpreter
	// runtime tests swap in (standalone_test.go's TestMain), and the compiler
	// runtime in production — exactly as NewRegistry does.
	rt := newRuntime(ctx)
	defer func() { _ = rt.Close(ctx) }()

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return sdk.Specification{}, cerrors.Errorf("failed to instantiate WASI: %w", err)
	}
	compiledHostModule, err := wazergo.Compile(ctx, rt, hostModule)
	if err != nil {
		return sdk.Specification{}, cerrors.Errorf("failed to compile host module: %w", err)
	}

	// A minimal, directory-less Registry: loadBlueprint reads only these four
	// fields. The schema service is a throwaway in-memory one — Specification()
	// never exercises a schema host call, and the throwaway processor is torn
	// down immediately inside loadBlueprint, so nothing is ever written to it.
	r := &Registry{
		logger:        logger,
		runtime:       rt,
		hostModule:    compiledHostModule,
		schemaService: schema.NewInMemoryService(),
	}

	bp, err := r.loadBlueprint(ctx, path)
	if err != nil {
		return sdk.Specification{}, err
	}
	// loadBlueprint returns the compiled module still open (a live Registry
	// keeps it for later NewProcessor calls). Here there is no Registry to own
	// it, so close it before the runtime is torn down.
	_ = bp.module.Close(ctx)

	return bp.specification, nil
}
