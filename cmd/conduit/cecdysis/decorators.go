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
	"path/filepath"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

		grpcAddress, tokenOpt, err := getAPITarget(cmd)
		if err != nil {
			return cerrors.Errorf("error reading gRPC address: %w", err)
		}

		client, err := api.NewClient(cmd.Context(), grpcAddress, tokenOpt)
		if err != nil {
			return err
		}
		defer client.Close()

		ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)
		return v.ExecuteWithClient(ctx, client)
	}

	return nil
}

// ------------------- CommandWithClient + result

// CommandWithExecuteWithClientResult is like CommandWithExecuteWithClient but its
// execution returns a result value that the framework renders: as JSON when the
// --json flag is set (proto responses via protojson, otherwise via go-json), and
// human-readable via Render otherwise. This gives client commands uniform --json
// output without per-command flag plumbing.
type CommandWithExecuteWithClientResult interface {
	ecdysis.Command

	// ExecuteWithClientResult does the work and returns the result to render. It
	// must not write the result to output itself.
	ExecuteWithClientResult(context.Context, *api.Client) (any, error)
	// Render returns the human-readable rendering of a result. Not called for --json.
	Render(result any) string
}

// CommandWithExecuteWithClientResultDecorator adds a Conduit API client and
// renders the command's result as JSON (--json) or human-readable.
type CommandWithExecuteWithClientResultDecorator struct{}

func (CommandWithExecuteWithClientResultDecorator) Decorate(_ *ecdysis.Ecdysis, cmd *cobra.Command, c ecdysis.Command) error {
	v, ok := c.(CommandWithExecuteWithClientResult)
	if !ok {
		return nil
	}

	if cmd.Flags().Lookup("json") == nil {
		cmd.Flags().Bool("json", false, "output the result as JSON")
	}

	old := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if old != nil {
			if err := old(cmd, args); err != nil {
				return err
			}
		}

		grpcAddress, tokenOpt, err := getAPITarget(cmd)
		if err != nil {
			return cerrors.Errorf("error reading gRPC address: %w", err)
		}

		client, err := api.NewClient(cmd.Context(), grpcAddress, tokenOpt)
		if err != nil {
			return err
		}
		defer client.Close()

		ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)
		result, err := v.ExecuteWithClientResult(ctx, client)
		if err != nil {
			return err
		}

		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			b, err := marshalJSON(result)
			if err != nil {
				return cerrors.Errorf("could not marshal result to JSON: %w", err)
			}
			// cmd.Println/cmd.Print resolve through cobra's OutOrStderr, not
			// OutOrStdout (see cobra's own doc on Print: "fallback to
			// Stderr if not set") — and no production call site ever calls
			// cmd.SetOut, so using them here put --json output on stderr
			// instead of stdout. Write to OutOrStdout explicitly instead;
			// see cecdysis.CommandWithResultDecorator for the same fix
			// applied to this decorator's offline sibling.
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		}

		fmt.Fprint(cmd.OutOrStdout(), v.Render(result))
		return nil
	}

	return nil
}

// MarshalJSON renders result exactly as CommandWithExecuteWithClientResultDecorator's
// --json path would (proto messages via protojson, matching the HTTP API's JSON
// shape; everything else via indented go-json). Exported so Family B command
// tests across cmd/conduit/root/* can assert on the exact bytes their command's
// --json flag would produce — e.g. to prove those bytes do NOT conform to the
// Family A envelope schema (see cmd/conduit/internal/testutils/envelope.go and
// cmd/conduit/cli/schema_golden_test.go) — without re-deriving this marshaling
// choice ad hoc per test.
func MarshalJSON(result any) ([]byte, error) {
	return marshalJSON(result)
}

// marshalJSON renders a result as indented JSON. Proto messages use protojson so
// the output matches the HTTP API's JSON shape; everything else uses go-json.
func marshalJSON(result any) ([]byte, error) {
	if pm, ok := result.(proto.Message); ok {
		b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(pm)
		if err != nil {
			return nil, cerrors.Errorf("protojson marshal: %w", err)
		}
		return b, nil
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, cerrors.Errorf("json marshal: %w", err)
	}
	return b, nil
}

// ProtoJSON marshals a proto message to its canonical protojson form as a
// json.RawMessage, for embedding proto values inside a composite --json result
// (e.g. a describe view assembled from several API calls). Using this keeps such
// results consistent with the protojson shape the single-message commands emit —
// enums as names, timestamps as RFC3339 — instead of go-json's struct encoding.
func ProtoJSON(m proto.Message) (json.RawMessage, error) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, cerrors.Errorf("protojson marshal: %w", err)
	}
	return json.RawMessage(b), nil
}

// ProtoJSONSlice is ProtoJSON over a slice, preserving order.
func ProtoJSONSlice[T proto.Message](ms []T) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(ms))
	for i, m := range ms {
		raw, err := ProtoJSON(m)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}

// getAPITarget returns the gRPC address and token source configured by the
// user (flag, environment variable or config file). If no address is found, the
// default address is returned. A token source that cannot be resolved is
// returned as the client option anyway, so the error surfaces from NewClient
// with its stable code instead of being swallowed here.
func getAPITarget(cmd *cobra.Command) (string, api.ClientOption, error) {
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

	// If it can't be parsed, we return the default value (and no explicit
	// token source: NewClient then falls back to the environment).
	err = ecdysis.ParseConfig(cfg, cmd)
	if err != nil {
		return defaultConfigValues.API.GRPC.Address, api.WithTokenSource("", ""), nil
	}
	addr := usrCfg.API.GRPC.Address
	if addr == "" {
		addr = defaultConfigValues.API.GRPC.Address
	}
	return addr, api.WithTokenSource(usrCfg.API.Token, usrCfg.API.TokenFile), nil
}
