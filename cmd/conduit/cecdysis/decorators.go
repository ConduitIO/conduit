// Copyright © 2025 Meroxa, Inc.
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

package cecdysis

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ------------------- CommandWithClient

// CommandWithExecuteWithClient can be implemented by a command that requires a client to interact
// with the Conduit API during the execution.
type CommandWithExecuteWithClient interface {
	ecdysis.Command

	// ExecuteWithClient is the actual work function. Most commands will implement this.
	ExecuteWithClient(context.Context, *api.Client) error
}

// CommandWithExecuteWithClientDecorator is a decorator that adds a Conduit API client to the command execution.
type CommandWithExecuteWithClientDecorator struct{}

func (CommandWithExecuteWithClientDecorator) Decorate(_ *ecdysis.Ecdysis, cmd *cobra.Command, c ecdysis.Command) error {
	v, ok := c.(CommandWithExecuteWithClient)
	if !ok {
		return nil
	}

	old := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if old != nil {
			err := old(cmd, args)
			if err != nil {
				return err
			}
		}

		grpcAddress, err := getGRPCAddress(cmd)
		if err != nil {
			return fmt.Errorf("error reading gRPC address: %w", err)
		}

		client, err := api.NewClient(cmd.Context(), grpcAddress)
		if err != nil {
			// Not an error we need to escalate to the main CLI execution. We'll print it out and not execute further.
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			return nil
		}
		defer client.Close()

		ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)
		return v.ExecuteWithClient(ctx, client)
	}

	return nil
}

func getGRPCAddress(cmd *cobra.Command) (string, error) {
	var (
		path string
		err  error
	)

	path, err = cmd.Flags().GetString("config.path")
	if err != nil || path == "" {
		path = conduit.DefaultConfig().ConduitCfgPath
	}

	// TODO REMOVE
	type Config struct {
		Path string
		API  struct {
			GRPC struct {
				Address string
			}
		}
	}

	var usrCfg *Config
	defaultConfigValues := conduit.DefaultConfigWithBasePath(path)

	cfg := ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &usrCfg,
		Path:          path,
		DefaultValues: defaultConfigValues,
	}

	viper := viper.New()

	viper.SetDefault("config.path", path)
	viper.SetDefault("api.grpc.address", defaultConfigValues.API.GRPC.Address)

	if err := ecdysis.ParseConfig(viper, cfg, cmd); err != nil {
		return "", fmt.Errorf("error parsing config: %w", err)
	}

	if err := viper.Unmarshal(cfg.Parsed); err != nil {
		return "", fmt.Errorf("error unmarshalling config: %w", err)
	}

	return usrCfg.API.GRPC.Address, nil
}
