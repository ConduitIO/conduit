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

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/log"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestDoctor_MatchesCLIDoctorEngine is the AC-7 regression test: the MCP
// doctor tool's result equals what running the CLI's own check set
// (doctorcheck.DefaultChecks + check.Run — cmd/conduit/root/doctor.go's
// ExecuteWithResult, reproduced here since that method is unexported to its
// package) produces for the identical conduit.Config/Options — proving the
// shared-engine rule: the MCP tool is not a reimplementation, it is the same
// check.Check slice through the same check.Run.
func TestDoctor_MatchesCLIDoctorEngine(t *testing.T) {
	is := is.New(t)

	cfg := conduit.DefaultConfigWithBasePath(t.TempDir())
	srv := NewServer(Config{StoreConfig: cfg})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDoctor,
		Arguments: map[string]any{},
	})
	is.NoErr(err)

	env := decodeStructuredContent[Result[DoctorResult]](t, res)

	// The CLI's own path: doctorcheck.DefaultChecks(cfg, log.Nop(), opts) +
	// check.Run(ctx, checks) — see cmd/conduit/root/doctor.go
	// ExecuteWithResult, which this reproduces with the same zero-value
	// Options the MCP call above used (Deep/RequireServer both false, no
	// Checks filter).
	wantChecks := doctorcheck.DefaultChecks(cfg, log.Nop(), doctorcheck.Options{})
	wantReport := check.Run(context.Background(), wantChecks)

	is.Equal(len(env.Result.Checks), len(wantReport.Checks))
	for i, got := range env.Result.Checks {
		want := wantReport.Checks[i]
		is.Equal(got.Name, want.Name)
		is.Equal(got.Status, want.Status)
		is.Equal(got.Code, want.Code)
		is.Equal(got.Category, want.Category)
	}
}

// TestDoctor_UnknownCheckName_RejectedWithSuggestion covers AC-6 for the
// doctor tool specifically: an unknown Checks entry is refused with a coded,
// suggestion-carrying error, mirroring the CLI's own validateCheckNames.
func TestDoctor_UnknownCheckName_RejectedWithSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{StoreConfig: conduit.DefaultConfigWithBasePath(t.TempDir())})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDoctor,
		Arguments: map[string]any{"checks": []string{"not.a.real.check"}},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[DoctorResult]](t, res)
	is.True(env.Error != nil)
	is.True(env.Error.Code != "")
	is.True(env.Error.Suggestion != "")
}

// TestDoctor_StandaloneEnv_NamesNotReturned asserts the MCP result (which
// reaches the client and the model provider behind it) carries a count for
// plugins.standalone_env but never the variable names or values. The
// environment is a fixed fake; the first and last flagged names in sorted order
// are both present so a leak of either is caught.
func TestDoctor_StandaloneEnv_NamesNotReturned(t *testing.T) {
	is := is.New(t)
	fake := []string{
		"AAA_FIRST_TOKEN=VALUE-MUST-NOT-APPEAR-1",
		"SENTINEL_API_TOKEN=VALUE-MUST-NOT-APPEAR-2",
		"SENTINEL_ONLY_NAME_PASSWORD=VALUE-MUST-NOT-APPEAR-3",
		"zzz_last_password=VALUE-MUST-NOT-APPEAR-4",
		"PATH=VALUE-MUST-NOT-APPEAR-5",
	}

	cfg := conduit.DefaultConfigWithBasePath(t.TempDir())
	cfg.Connectors.Path = t.TempDir()
	is.NoErr(os.WriteFile(filepath.Join(cfg.Connectors.Path, "conduit-connector-fake"), []byte("#!/bin/sh\n"), 0o755))

	cs := connectTestClient(t, NewServer(Config{StoreConfig: cfg, environ: func() []string { return fake }}))
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDoctor,
		Arguments: map[string]any{"checks": []string{doctorcheck.NamePluginsStandaloneEnv}},
	})
	is.NoErr(err)

	env := decodeStructuredContent[Result[DoctorResult]](t, res)
	is.Equal(len(env.Result.Checks), 1)
	got := env.Result.Checks[0]
	is.Equal(string(got.Status), "warn")
	is.Equal(got.Code, "plugin.standalone_env_passed")
	is.True(strings.Contains(got.Message, "withheld"))
	is.True(strings.Contains(got.Message, "4 host environment variable(s)"))

	// Everything the client can see: structured content and text content.
	raw, err := json.Marshal(res)
	is.NoErr(err)
	for _, kv := range fake {
		name, value, _ := strings.Cut(kv, "=")
		is.True(!strings.Contains(string(raw), value))
		if name != "PATH" {
			is.True(!strings.Contains(string(raw), name))
		}
	}
}

// TestDoctor_StandaloneEnv_DefaultEnvironIsProcessEnvironment pins the
// production default for the MCP tool: with no injected environment it
// classifies the process environment. The result withholds names, so the
// assertion is that the finding is a warning (an empty environment passes) and
// that neither the sentinel name nor its fake value is returned.
func TestDoctor_StandaloneEnv_DefaultEnvironIsProcessEnvironment(t *testing.T) {
	is := is.New(t)
	const name, value = "SENTINEL_DEFAULT_SEAM_TOKEN", "FAKE-VALUE-MUST-NOT-APPEAR-DEFAULT"
	t.Setenv(name, value)

	cfg := conduit.DefaultConfigWithBasePath(t.TempDir())
	cfg.Connectors.Path = t.TempDir()
	is.NoErr(os.WriteFile(filepath.Join(cfg.Connectors.Path, "conduit-connector-fake"), []byte("#!/bin/sh\n"), 0o755))

	cs := connectTestClient(t, NewServer(Config{StoreConfig: cfg}))
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDoctor,
		Arguments: map[string]any{"checks": []string{doctorcheck.NamePluginsStandaloneEnv}},
	})
	is.NoErr(err)
	env := decodeStructuredContent[Result[DoctorResult]](t, res)
	is.Equal(len(env.Result.Checks), 1)
	is.Equal(string(env.Result.Checks[0].Status), "warn")

	raw, err := json.Marshal(res)
	is.NoErr(err)
	is.True(!strings.Contains(string(raw), name))
	is.True(!strings.Contains(string(raw), value))
}
