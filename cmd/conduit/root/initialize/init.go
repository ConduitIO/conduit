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

package initialize

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/conduitio/yaml/v3"
)

var (
	_ cecdysis.CommandWithResult = (*InitCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InitCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*InitCommand)(nil)
)

type InitFlags struct {
	Path string `long:"path" usage:"Path where Conduit will be initialized." default:"."`
}

type InitCommand struct {
	flags InitFlags
	Cfg   *conduit.Config
}

func (c *InitCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	flags.SetDefault("path", filepath.Dir(c.Cfg.ConduitCfg.Path))
	return flags
}

func (c *InitCommand) Usage() string { return "init" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: `Set up a Conduit workspace: the config file and directories.`,
		Long: `Set up a Conduit workspace in the current directory: write a conduit.yaml config
file and create the pipelines, connectors, and processors directories.

This prepares the workspace only — it does not create a pipeline. To scaffold a
runnable pipeline afterward, use 'conduit pipelines init'.`,
	}
}

// dirResult is one directory createDirs attempted to create — part of
// Outcome.Result's InitResult, and what Render's human-mode text derives
// from (kept structured, not printed directly, so --json doesn't have to
// re-parse human text — see CLI output conventions §5: --json output must
// never be interleaved with direct stdout writes from the work function).
type dirResult struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

// InitResult is Outcome.Result's concrete shape for `conduit init`.
type InitResult struct {
	Dirs       []dirResult `json:"dirs"`
	ConfigPath string      `json:"configPath"`
}

func (c *InitCommand) createDirs() ([]dirResult, error) {
	// These could be used based on the root flags if those were global
	dirs := []string{"processors", "connectors", "pipelines"}
	results := make([]dirResult, 0, len(dirs))

	for _, dir := range dirs {
		path := filepath.Join(c.flags.Path, dir)

		// Attempt to create the directory, skipping if it already exists
		if err := os.Mkdir(path, os.ModePerm); err != nil {
			if os.IsExist(err) {
				results = append(results, dirResult{Path: path, Created: false})
				continue
			}
			return nil, cerrors.Errorf("failed to create directory '%s': %w", path, err)
		}

		results = append(results, dirResult{Path: path, Created: true})
	}

	return results, nil
}

func (c *InitCommand) createConfigYAML() (string, error) {
	cfgYAML := internal.NewYAMLTree()
	processConfigStruct(reflect.ValueOf(c.Cfg).Elem(), "", cfgYAML)

	// Create encoder with custom indentation
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Set indentation to 2 spaces

	err := encoder.Encode(cfgYAML.Root)
	if err != nil {
		return "", cerrors.Errorf("error marshaling YAML: %w", err)
	}

	conduitYAML := filepath.Join(c.flags.Path, "conduit.yaml")
	err = os.WriteFile(conduitYAML, buf.Bytes(), 0o600)
	if err != nil {
		return "", cerrors.Errorf("error writing conduit.yaml: %w", err)
	}

	return conduitYAML, nil
}

func processConfigStruct(v reflect.Value, parentPath string, cfgYAML *internal.YAMLTree) {
	if v.Kind() == reflect.Pointer {
		v = v.Elem() // Dereference pointer to get the actual value
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)
		longName := field.Tag.Get("long")

		// Determine the full path for the YAML key
		fullPath := longName
		if parentPath != "" && longName != "" {
			fullPath = parentPath + "." + longName
		}

		// Recursively process struct fields
		if fieldValue.Kind() == reflect.Struct || (fieldValue.Kind() == reflect.Pointer && !fieldValue.IsNil() && fieldValue.Elem().Kind() == reflect.Struct) {
			processConfigStruct(fieldValue, fullPath, cfgYAML)
			continue
		}

		// Process non-struct fields with a long tag
		if longName != "" {
			value := fmt.Sprintf("%v", fieldValue.Interface())
			usage := field.Tag.Get("usage")
			if value != "" { // Only insert non-empty values
				cfgYAML.Insert(fullPath, value, usage)
			}
		}
	}
}

// ResultCommand returns the --json envelope's stable dotted discriminator.
func (c *InitCommand) ResultCommand() string { return "init" }

// ExecuteWithResult sets up the Conduit workspace (directories + conduit.yaml)
// and returns the shared cecdysis.Outcome envelope. A non-nil error return is
// a HARD command failure (e.g. an unwritable directory) — there are no domain
// findings for this command; every successful run is OK:true.
func (c *InitCommand) ExecuteWithResult(_ context.Context) (cecdysis.Outcome, error) {
	dirs, err := c.createDirs()
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	configPath, err := c.createConfigYAML()
	if err != nil {
		return cecdysis.Outcome{}, cerrors.Errorf("failed to create config YAML: %w", err)
	}

	return cecdysis.Outcome{
		OK:     true,
		Result: InitResult{Dirs: dirs, ConfigPath: configPath},
	}, nil
}

// Render returns the human-readable rendering of a successful `conduit init`
// run — the same informational text the command printed directly before
// this command was migrated onto cecdysis.CommandWithResult (see cli-contract.md
// §7's backward-compat mitigation: only --json is new, human-mode output is
// unchanged).
func (c *InitCommand) Render(outcome cecdysis.Outcome) string {
	result, _ := outcome.Result.(InitResult)

	var b strings.Builder
	for _, d := range result.Dirs {
		if d.Created {
			fmt.Fprintf(&b, "Created directory: %s\n", d.Path)
		} else {
			fmt.Fprintf(&b, "Directory '%s' already exists, skipping...\n", d.Path)
		}
	}
	fmt.Fprintf(&b, "Config file written to %v\n", result.ConfigPath)
	b.WriteString(`
Conduit workspace is ready.

Next: create a pipeline
  conduit pipelines init        a runnable demo pipeline (add --help to customize)

Then start Conduit
  conduit run
`)
	return b.String()
}
