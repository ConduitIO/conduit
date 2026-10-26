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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/spf13/cobra"
	grpcstatus "google.golang.org/grpc/status"
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
		asJSON, _ := cmd.Flags().GetBool("json")
		result, err := runClientResult(cmd, args, old, v)
		if err != nil {
			if asJSON {
				// Under --json a failure is one JSON object on stdout too,
				// so a script reads the code from it instead of parsing
				// cobra's "Error: ..." text (#2989). The error is still
				// returned, so the exit code is unchanged. Cobra's own
				// error line (stderr) and usage text (stdout, which would
				// corrupt the object) are silenced for this run only.
				renderClientResultError(cmd, err)
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
			}
			return err
		}

		if asJSON {
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

// runClientResult runs the decorated command: any earlier RunE in the chain,
// then the client dial, then ExecuteWithClientResult.
func runClientResult(cmd *cobra.Command, args []string, old func(*cobra.Command, []string) error, v CommandWithExecuteWithClientResult) (any, error) {
	if old != nil {
		if err := old(cmd, args); err != nil {
			return nil, err
		}
	}

	grpcAddress, tokenOpt, err := getAPITarget(cmd)
	if err != nil {
		return nil, cerrors.Errorf("error reading gRPC address: %w", err)
	}

	client, err := api.NewClient(cmd.Context(), grpcAddress, tokenOpt)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)
	return v.ExecuteWithClientResult(ctx, client)
}

// ClientResultError is the --json shape a client-result (Family B) command
// prints on stdout when it fails: {"error": {...}}. Success output is the raw
// result value and is not wrapped; see
// docs/design-documents/20260707-cli-output-conventions.md §6. The error
// fields are the envelope's ResultError, so both families report a failure
// with the same code, message, suggestion and config path.
type ClientResultError struct {
	Error *ResultError `json:"error"`
}

// NewClientResultError converts a client command's error into the --json
// error shape. A server error crosses gRPC as a status whose ErrorInfo detail
// carries the stable code, suggestion and config path; it is decoded with
// conduiterr.FromStatus. A ConduitError raised in the CLI itself is used as
// is. Anything else is reported as common.unknown with its message.
func NewClientResultError(err error) ClientResultError {
	if _, ok := conduiterr.Get(err); ok {
		return ClientResultError{Error: newResultError(err)}
	}
	// errors.As, not grpcstatus.FromError: FromError on a wrapped status
	// replaces the server's message with the whole wrapped error text.
	var se interface{ GRPCStatus() *grpcstatus.Status }
	if cerrors.As(err, &se) {
		return ClientResultError{Error: newResultError(conduiterr.FromStatus(se.GRPCStatus()))}
	}
	return ClientResultError{Error: newResultError(err)}
}

// renderClientResultError writes NewClientResultError(err) to stdout as one
// JSON object. If marshaling fails (it can't for this struct of strings), it
// falls back to the plain error on stderr rather than printing nothing.
func renderClientResultError(cmd *cobra.Command, err error) {
	b, merr := json.MarshalIndent(NewClientResultError(err), "", "  ")
	if merr != nil {
		cmd.PrintErrln("Error:", err)
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
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
