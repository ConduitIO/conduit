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

package harness

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolKCMigrate is the MCP tool the design names for `migrate kafka-connect`
// ("MCP tool parity").
const ToolKCMigrate = "kc_migrate"

// MCP drives `conduit mcp` over stdio and calls kc_migrate. It is content in,
// content out, as the design requires: input files are sent by content with
// their labels as names, and the files the tool returns are compared with
// the files the CLI writes.
type MCP struct {
	Bin string
}

func (m MCP) Name() string { return "mcp" }

// Available connects and lists tools; kc_migrate missing is pending (#3003,
// lane-plan PR 15).
func (m MCP) Available(ctx context.Context) error {
	if m.Bin == "" {
		return cerrors.Errorf("CONDUIT_BIN is not set: %w", ErrPending)
	}
	s, closeFn, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		return cerrors.Errorf("mcp tools/list: %w", err)
	}
	for _, t := range res.Tools {
		if t.Name == ToolKCMigrate {
			return nil
		}
	}
	return cerrors.Errorf("`conduit mcp` does not register %s (#3003, lane-plan PR 15): %w", ToolKCMigrate, ErrPending)
}

// Migrate maps the case's command line onto the tool's input. A flag the
// tool does not expose makes the case ineligible for parity, reported as an
// error rather than silently dropped from the call.
func (m MCP) Migrate(ctx context.Context, req Request) (Output, error) {
	args, err := m.arguments(req)
	if err != nil {
		return Output{}, err
	}
	s, closeFn, err := m.connect(ctx)
	if err != nil {
		return Output{}, err
	}
	defer closeFn()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: ToolKCMigrate, Arguments: args})
	if err != nil {
		return Output{}, cerrors.Errorf("mcp tools/call %s: %w", ToolKCMigrate, err)
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return Output{}, cerrors.Errorf("marshal structured content: %w", err)
	}
	return splitMCPFiles(sc)
}

type mcpFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (m MCP) arguments(req Request) (map[string]any, error) {
	labels := commonParentLabels(req.Case.Inputs())
	label := map[string]string{}
	for i, in := range req.Case.Inputs() {
		label[in] = labels[i]
	}
	var inputs []mcpFile
	out := map[string]any{}
	args := req.Case.Manifest.Command.Args[2:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--worker" && i+1 < len(args):
			f, err := readInput(req.Case.Dir, args[i+1], label[args[i+1]])
			if err != nil {
				return nil, err
			}
			out["worker"] = f
			i++
		case a == "--strict":
			out["strict"] = true
		case a == "--dry-run":
			out["dry_run"] = true
		case strings.HasPrefix(a, "input/"):
			f, err := readInput(req.Case.Dir, a, label[a])
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, f)
		default:
			return nil, cerrors.Errorf("flag %s has no %s counterpart; the case is not eligible for MCP parity", a, ToolKCMigrate)
		}
	}
	out["inputs"] = inputs
	return out, nil
}

func readInput(caseDir, rel, label string) (mcpFile, error) {
	b, err := os.ReadFile(filepath.Join(caseDir, filepath.FromSlash(rel)))
	if err != nil {
		return mcpFile{}, cerrors.Errorf("read %s: %w", rel, err)
	}
	if label == "" {
		label = path.Base(rel)
	}
	return mcpFile{Name: label, Content: string(b)}, nil
}

// splitMCPFiles separates the returned files from the envelope. The design
// says the tool returns the CLI's result "plus files[] of {path, content}"
// without fixing where files sits; both placements are accepted until the
// CLI lane's tool schema pins one.
func splitMCPFiles(sc []byte) (Output, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(sc, &env); err != nil {
		return Output{}, cerrors.Errorf("structured content is not an object: %w", err)
	}
	files := map[string][]byte{}
	take := func(raw json.RawMessage) error {
		var fs []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &fs); err != nil {
			return cerrors.Errorf("files: %w", err)
		}
		for _, f := range fs {
			files[f.Path] = []byte(f.Content)
		}
		return nil
	}
	if raw, ok := env["files"]; ok {
		if err := take(raw); err != nil {
			return Output{}, err
		}
		delete(env, "files")
	}
	if raw, ok := env["result"]; ok {
		var result map[string]json.RawMessage
		if json.Unmarshal(raw, &result) == nil {
			if fr, ok := result["files"]; ok {
				if err := take(fr); err != nil {
					return Output{}, err
				}
				delete(result, "files")
				b, err := json.Marshal(result)
				if err != nil {
					return Output{}, cerrors.Errorf("marshal result: %w", err)
				}
				env["result"] = b
			}
		}
	}
	stdout, err := json.Marshal(env)
	if err != nil {
		return Output{}, cerrors.Errorf("marshal envelope: %w", err)
	}
	return Output{Stdout: stdout, Files: files}, nil
}

func (m MCP) connect(ctx context.Context) (*mcp.ClientSession, func(), error) {
	cmd := exec.CommandContext(ctx, m.Bin, "mcp") //nolint:gosec // the binary under test, chosen by the test runner
	cmd.Env = fixedEnv(scratchHome())
	cmd.WaitDelay = 5 * time.Second
	client := mcp.NewClient(&mcp.Implementation{Name: "conduit-migrate-harness", Version: "1"}, nil)
	cctx, cancel := context.WithTimeout(ctx, runTimeout)
	s, err := client.Connect(cctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		return nil, nil, cerrors.Errorf("start `conduit mcp`: %w", err)
	}
	return s, func() { _ = s.Close(); cancel() }, nil
}
