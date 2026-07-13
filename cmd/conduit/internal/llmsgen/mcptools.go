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

package main

import "github.com/conduitio/conduit/cmd/conduit/internal/mcp"

// gatherMCPTools returns mcp.Catalog() unchanged — it is already sorted by
// Name and is the single source of truth NewServer registers tools from
// (design doc D4), so there is nothing left for the generator to compute.
// This wrapper exists so every "gather a source" function in this package
// has the same shape and the same place to add generator-side logic later
// if the catalog ever needs it (e.g. an input-schema summary).
func gatherMCPTools() []mcp.ToolInfo {
	return mcp.Catalog()
}
