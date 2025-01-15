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

package config

import (
	"context"
	"fmt"
	"reflect"

	"github.com/conduitio/conduit/cmd/conduit/root/run"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithExecute = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*ConfigCommand)(nil)
)

type ConfigCommand struct {
	RunCmd *run.RunCommand
}

func (c *ConfigCommand) Config() ecdysis.Config {
	return c.RunCmd.Config()
}

func (c *ConfigCommand) Flags() []ecdysis.Flag {
	return c.RunCmd.Flags()
}

func (c *ConfigCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Shows the configuration to be used when running Conduit.",
		Long: `Conduit will run based on the default configuration jointly with a provided configuration file (optional), 
the set environment variables, and the flags used. This command will show the configuration that will be used.`,
	}
}

func printStruct(ctx context.Context, v reflect.Value, parentPath string) {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)
		longName := field.Tag.Get("long")

		fullPath := longName
		if parentPath != "" && longName != "" {
			fullPath = parentPath + "." + longName
		}

		if fieldValue.Kind() == reflect.Struct ||
			(fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() && fieldValue.Elem().Kind() == reflect.Struct) {
			printStruct(ctx, fieldValue, fullPath)
			continue
		}

		if longName != "" {
			value := fmt.Sprintf("%v", fieldValue.Interface())
			if value != "" {
				cobraCmd := ecdysis.CobraCmdFromContext(ctx)
				_, err := fmt.Fprintf(cobraCmd.OutOrStdout(), "%s: %s\n", fullPath, value)
				if err != nil {
					fmt.Printf("failed writing config value to out: %v", err)
				}
			}
		}
	}
}

func (c *ConfigCommand) Usage() string { return "config" }

func (c ConfigCommand) Execute(ctx context.Context) error {
	printStruct(ctx, reflect.ValueOf(c.RunCmd.Cfg), "")
	return nil
}
