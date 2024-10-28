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

package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

type InitArgs struct {
	Path string
}

type ConduitInit struct {
	Args InitArgs
}

func NewConduitInit(args InitArgs) *ConduitInit {
	// set defaults
	if args.Path == "" {
		args.Path = "."
	}
	return &ConduitInit{Args: args}
}

func (i *ConduitInit) Run() error {
	err := i.createDirs()
	if err != nil {
		return err
	}

	err = i.createConfigYAML()
	if err != nil {
		return fmt.Errorf("failed to create config YAML: %w", err)
	}

	fmt.Println("Conduit has been initialized!")
	fmt.Println("To quickly create an example pipeline, run `conduit pipelines init`.")
	fmt.Println("To see how you can customize your first pipeline, run `conduit pipelines init --help`.")
	return nil
}

func (i *ConduitInit) createConfigYAML() error {
	return nil
}

func (i *ConduitInit) createDirs() error {
	dirs := []string{"processors", "connectors", "pipelines"}

	for _, dir := range dirs {
		path := filepath.Join(i.Args.Path, dir)

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
