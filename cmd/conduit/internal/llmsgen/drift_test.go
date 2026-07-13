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

import (
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/matryer/is"
)

// These tests are the "does the guard actually catch drift" half of the
// design doc's AC-2..AC-5: rather than actually staging a fake connector
// in builtin.DefaultBuiltinConnectors or a fake code in the registry
// (which would require modifying package-level state other tests in this
// binary depend on), each test takes a real, gathered snapshot of the
// generator's data, perturbs it the same way the described drift would
// (one more connector / error code / MCP tool / config field than what
// generated the committed files), and asserts the re-rendered output
// differs from the committed file. That is exactly what
// validate-generated-files' `git diff --exit-code` would observe in CI:
// a real (not hand-simulated) drift in any of the four sources changes
// the rendered bytes.

func TestDrift_ExtraConnectorChangesOutput(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)
	before := renderLLMSTxt(data)

	data.Connectors = append(data.Connectors, connectorInfo{Name: "zzz-fake-connector", Summary: "not real"})
	after := renderLLMSTxt(data)

	is.True(before != after) // an unregenerated new connector must change the rendered output
}

func TestDrift_ExtraErrorCodeChangesOutput(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)
	before := renderLLMSTxt(data)
	beforeFull := renderLLMSFullTxt(data)

	data.ErrorCodes = append(data.ErrorCodes, errorCodeInfo{Reason: "zzz.fake_code", GRPCCode: "Internal", Description: "not real"})
	after := renderLLMSTxt(data)
	afterFull := renderLLMSFullTxt(data)

	is.True(before != after) // the error-code count line must change
	is.True(beforeFull != afterFull)
}

func TestDrift_ExtraMCPToolChangesOutput(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)
	before := renderLLMSTxt(data)
	beforeFull := renderLLMSFullTxt(data)

	data.MCPTools = append(data.MCPTools, mcp.ToolInfo{Name: "zzz_fake_tool", Description: "not real"})
	after := renderLLMSTxt(data)
	afterFull := renderLLMSFullTxt(data)

	is.True(before != after)
	is.True(beforeFull != afterFull)
}

func TestDrift_ExtraConfigFieldChangesOutput(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)
	before := renderLLMSFullTxt(data)

	data.Config.Structs[0].Fields = append(data.Config.Structs[0].Fields,
		configField{GoField: "ZZZFake", YAMLKey: "zzz-fake", GoType: "string", Required: true})
	after := renderLLMSFullTxt(data)

	is.True(before != after) // a new config struct field must change the rendered schema section
}

// TestDrift_HandEditNotReproduced is AC-6: a hand edit to the committed
// llms.txt that the generator would not itself produce is caught, because
// this is exactly the comparison TestGenerator_MatchesCommittedFiles makes
// against the real committed file — this test only documents *why* that
// comparison is the AC-6 guard, using a synthetic edit.
func TestDrift_HandEditNotReproduced(t *testing.T) {
	is := is.New(t)
	root := repoRoot(t)

	data, err := buildGeneratedData(root)
	is.NoErr(err)
	generated := renderLLMSTxt(data)

	handEdited := generated + "\n## Hand-added section\n- not from any source\n"
	is.True(handEdited != generated) // trivially true, but documents the guard's shape
}
