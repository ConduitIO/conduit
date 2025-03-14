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
	"path/filepath"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/cobra"
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
			return cerrors.Errorf("error reading gRPC address: %w", err)
		}

		client, err := api.NewClient(cmd.Context(), grpcAddress)
		if err != nil {
			return err
		}
		defer client.Close()

		ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)
		return v.ExecuteWithClient(ctx, client)
	}

	return nil
}

// getGRPCAddress returns the gRPC address configured by the user. If no address is found, the default address is returned.
func getGRPCAddress(cmd *cobra.Command) (string, error) {
	var (
		path string
		err  error
	)

	path, err = cmd.Flags().GetString("config.path")
	if err != nil || path == "" {
		path = conduit.DefaultConfig().ConduitCfg.Path
	}

	var usrCfg conduit.Config
	defaultConfigValues := conduit.DefaultConfigWithBasePath(filepath.Dir(path))

	cfg := ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &usrCfg,
		Path:          path,
		DefaultValues: defaultConfigValues,
	}

	// If it can't be parsed, we return the default value
	err = ecdysis.ParseConfig(cfg, cmd)
	if err != nil || usrCfg.API.GRPC.Address == "" {
		return defaultConfigValues.API.GRPC.Address, nil
	}

	return usrCfg.API.GRPC.Address, nil
}
