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
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/conduitio/yaml/v3"
)

var (
	_ ecdysis.CommandWithFlags   = (*InitCommand)(nil)
	_ ecdysis.CommandWithExecute = (*InitCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InitCommand)(nil)
)

type InitFlags struct {
	ConfigPath string `flag:"config.path" usage:"path where Conduit will be initialized"`
}

type InitCommand struct {
	flags InitFlags
}

func (c *InitCommand) Usage() string { return "init" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: `Initialize Conduit with a configuration file and directories.`,
	}
}

func (c *InitCommand) createDirs() error {
	dirs := []string{"processors", "connectors", "pipelines"}

	for _, dir := range dirs {
		path := filepath.Join(c.flags.ConfigPath, dir)

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

func (c *InitCommand) conduitCfgFlags() *flag.FlagSet {
	cfg := conduit.DefaultConfigWithBasePath(c.flags.ConfigPath)
	return conduit.Flags(&cfg)
}

func (c *InitCommand) createConfigYAML() error {
	cfgYAML := internal.NewYAMLTree()
	c.conduitCfgFlags().VisitAll(func(f *flag.Flag) {
		cfgYAML.Insert(f.Name, f.DefValue, f.Usage)
	})

	yamlData, err := yaml.Marshal(cfgYAML.Root)
	if err != nil {
		return cerrors.Errorf("error marshaling YAML: %w\n", err)
	}

	path := filepath.Join(c.flags.ConfigPath, "conduit.yaml")
	err = os.WriteFile(path, yamlData, 0o600)
	if err != nil {
		return cerrors.Errorf("error writing conduit.yaml: %w", err)
	}
	fmt.Printf("Configuration file written to %v\n", path)

	return nil
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

func (c *InitCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	// Set current working directory as default
	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	flags.SetDefault("config.path", currentPath)

	return flags
}
