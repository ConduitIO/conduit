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

package root

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/conduitio/yaml/v3"
)

var (
	_ ecdysis.CommandWithExecute = (*InitCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InitCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*InitCommand)(nil)
)

type InitFlags struct {
	Path string `long:"path" usage:"Path where Conduit will be initialized." default:"."`
}

type InitCommand struct {
	flags InitFlags
	cfg   *conduit.Config
}

func (c *InitCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	flags.SetDefault("path", filepath.Dir(c.cfg.ConduitCfgPath))
	return flags
}

func (c *InitCommand) Usage() string { return "init" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: `Initialize Conduit with a configuration file and directories.`,
	}
}

func (c *InitCommand) createDirs() error {
	// These could be used based on the root flags if those were global
	dirs := []string{"processors", "connectors", "pipelines"}
	conduitPath := filepath.Dir(c.flags.Path)

	for _, dir := range dirs {
		path := filepath.Join(conduitPath, dir)

		// Attempt to create the directory, skipping if it already exists
		if err := os.Mkdir(path, os.ModePerm); err != nil {
			if os.IsExist(err) {
				fmt.Printf("Directory '%s' already exists, skipping...\n", path)
				continue
			}
			return fmt.Errorf("failed to create directory '%s': %w", path, err)
		}

		fmt.Printf("Created directory: %s\n", path)
	}

	return nil
}

func (c *InitCommand) createConfigYAML() error {
	cfgYAML := internal.NewYAMLTree()
	processConfigStruct(reflect.ValueOf(c.cfg).Elem(), "", cfgYAML)

	// Create encoder with custom indentation
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Set indentation to 2 spaces

	err := encoder.Encode(cfgYAML.Root)
	if err != nil {
		return cerrors.Errorf("error marshaling YAML: %w\n", err)
	}

	conduitYAML := filepath.Join(c.flags.Path, "conduit.yaml")
	err = os.WriteFile(conduitYAML, buf.Bytes(), 0o600)
	if err != nil {
		return cerrors.Errorf("error writing conduit.yaml: %w", err)
	}
	fmt.Printf("Config file written to %v\n", conduitYAML)

	return nil
}

func processConfigStruct(v reflect.Value, parentPath string, cfgYAML *internal.YAMLTree) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		longName := field.Tag.Get("long")

		if fieldValue.Kind() == reflect.Struct {
			if longName != "" {
				fullPath := longName
				if parentPath != "" {
					fullPath = parentPath + "." + longName
				}
				processConfigStruct(fieldValue, fullPath, cfgYAML)
			} else {
				processConfigStruct(fieldValue, parentPath, cfgYAML)
			}
			continue
		}

		// For pointer types that point to structs
		if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() && fieldValue.Elem().Kind() == reflect.Struct {
			if longName != "" {
				fullPath := longName
				if parentPath != "" {
					fullPath = parentPath + "." + longName
				}
				processConfigStruct(fieldValue.Elem(), fullPath, cfgYAML)
			} else {
				processConfigStruct(fieldValue.Elem(), parentPath, cfgYAML)
			}
			continue
		}

		// For non-struct fields, only process if they have a long tag
		if longName != "" {
			fullPath := longName
			if parentPath != "" {
				fullPath = parentPath + "." + longName
			}
			value := fmt.Sprintf("%v", fieldValue.Interface())
			usage := field.Tag.Get("usage")
			if value != "" { // Only insert non-empty values
				cfgYAML.Insert(fullPath, value, usage)
			}
		}
	}
}

func (c *InitCommand) Execute(ctx context.Context) error {
	err := c.createDirs()
	if err != nil {
		return err
	}

	err = c.createConfigYAML()
	if err != nil {
		return fmt.Errorf("failed to create config YAML: %w", err)
	}

	fmt.Println(`
	Conduit has been initialized!
	
	To quickly create an example pipeline, run 'conduit pipelines init'.
	To see how you can customize your first pipeline, run 'conduit pipelines init --help'.`)

	return nil
}
