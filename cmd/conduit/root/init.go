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
)

type InitCommand struct {
	cfg       *conduit.Config
	rootFlags *RootFlags
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
	conduitPath := filepath.Dir(c.rootFlags.ConduitConfigPath)

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

	v := reflect.Indirect(reflect.ValueOf(c.cfg))
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		if fieldValue.Kind() == reflect.Struct {
			embedStructYAML(fieldValue, field, cfgYAML)
		} else {
			value := fmt.Sprintf("%v", fieldValue.Interface())
			usage := field.Tag.Get("usage")
			longName := field.Tag.Get("long")

			if longName != "" {
				cfgYAML.Insert(longName, value, usage)
			}
		}
	}

	yamlData, err := yaml.Marshal(cfgYAML.Root)
	if err != nil {
		return cerrors.Errorf("error marshaling YAML: %w\n", err)
	}

	err = os.WriteFile(c.rootFlags.ConduitConfigPath, yamlData, 0o600)
	if err != nil {
		return cerrors.Errorf("error writing conduit.yaml: %w", err)
	}
	fmt.Printf("Configuration file written to %v\n", c.rootFlags.ConduitConfigPath)

	return nil
}

func embedStructYAML(v reflect.Value, field reflect.StructField, cfgYAML *internal.YAMLTree) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		subField := t.Field(i)
		subFieldValue := v.Field(i)

		if subFieldValue.Kind() == reflect.Struct {
			embedStructYAML(subFieldValue, subField, cfgYAML)
		} else {
			value := fmt.Sprintf("%v", subFieldValue.Interface())
			usage := subField.Tag.Get("usage")
			longName := subField.Tag.Get("long")

			if longName != "" {
				cfgYAML.Insert(longName, value, usage)
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
