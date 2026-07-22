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

// Package allcodes is a blank-import barrel: importing it, and only it,
// guarantees every conduiterr.Code in the engine has been registered
// (conduiterr.Register runs at package-level var-init time — see
// conduiterr.Register's doc — so a package's codes only exist in the
// registry once something imports it).
//
// This is D5 of docs/design-documents/20260712-llms-txt-generation.md: the
// llms.txt generator must not silently under-report error codes just
// because it happens not to import every code-declaring package. Rather
// than hand-list codes.go files (which drifts as new ones are added), the
// generator's completeness is enforced two ways: this barrel gives the
// generator itself a fixed, reviewable import list, and
// llmsgen's TestAllCodesComplete independently re-derives the "true" set by
// statically scanning the repo for every Register(...) call-site — so a new
// codes-declaring package that is added to the engine but never wired into
// this barrel fails that test, not just an eyeball review of this file.
//
// # Deviation from the design doc's example path
//
// The design doc sketches this barrel at
// pkg/foundation/cerrors/conduiterr/allcodes. That path cannot compile: two
// of the twelve code-declaring files are cmd/conduit/internal/deploy/{deploy,
// service}.go, and Go's internal-package visibility rule only lets importers
// rooted under cmd/conduit/ import cmd/conduit/internal/deploy. A package
// under pkg/... can never import it. This barrel therefore lives under
// cmd/conduit/internal/llmsgen/ instead — still unexported outside the main
// module, and still able to import every one of the twelve files, which the
// pkg/-rooted location could not.
//
// # Why every import here is blank
//
// Every import is `_` (blank): this package's only use for each dependency
// is its init-time side effect (populating conduiterr's registry), never
// any of its exported symbols. Codes() below reads back through
// conduiterr.Codes(), not through any of these packages directly.
package allcodes

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"

	// Blank-imported solely for their package-level conduiterr.Register(...)
	// var initializers. Keep this list in sync with every codes.go (or
	// equivalently named file) in the engine — TestAllCodesComplete in
	// ../llmsgen fails the build if it drifts.
	_ "github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	_ "github.com/conduitio/conduit/pkg/connector"
	_ "github.com/conduitio/conduit/pkg/lifecycle-poc"
	_ "github.com/conduitio/conduit/pkg/orchestrator"
	_ "github.com/conduitio/conduit/pkg/pipeline"
	_ "github.com/conduitio/conduit/pkg/processor"
	_ "github.com/conduitio/conduit/pkg/provisioning"
	_ "github.com/conduitio/conduit/pkg/provisioning/config"
	_ "github.com/conduitio/conduit/pkg/registry"
	_ "github.com/conduitio/conduit/pkg/registry/index"
	_ "github.com/conduitio/conduit/pkg/registry/policy"
	_ "github.com/conduitio/conduit/pkg/registry/trust"
	_ "github.com/conduitio/conduit/pkg/scaffold"
)

// Codes returns every conduiterr.Code visible once every package this
// barrel blank-imports has run its init. It is a thin pass-through to
// conduiterr.Codes() (already sorted by reason); the barrel's value is
// entirely in the import list above, not in this function.
func Codes() []conduiterr.Code { return conduiterr.Codes() }
