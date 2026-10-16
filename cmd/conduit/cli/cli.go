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

package cli

import (
	"fmt"
	"os"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/viper"
)

func Run(cfg conduit.Config) {
	e := ecdysis.New(ecdysis.WithDecorators(
		cecdysis.CommandWithExecuteWithClientDecorator{},
		cecdysis.CommandWithExecuteWithClientResultDecorator{},
		// The offline sibling of CommandWithExecuteWithClientResultDecorator,
		// for commands (doctor, pipelines validate, connector/processor new)
		// whose own checks are the thing being run rather than a client call.
		cecdysis.CommandWithResultDecorator{},
	))

	cmd := e.MustBuildCobraCommand(&root.RootCommand{Cfg: cfg})
	cmd.CompletionOptions.DisableDefaultCmd = true

	// Don't want to show usage when there's some unexpected error executing the command
	// Help will still be shown via --help
	cmd.SilenceUsage = true

	// ExecuteC (not Execute) so a nil-error run can still report a non-OK
	// exit code for a domain finding — e.g. `doctor` reporting a failed
	// check with Outcome.OK == false but no HARD error (see
	// cecdysis.CommandWithResultExitCode). leaf is the actual subcommand
	// that ran; Annotations are per-Command, not inherited, so ResultExitCode
	// must be read off leaf, not cmd.
	leaf, err := cmd.ExecuteC()
	if err != nil {
		err = wrapConfigParseError(cfg, err)
		// error is already printed out by cobra; the exit code is the single
		// deterministic classifier shared with the `run` entrypoint (see
		// pkg/conduit/exitcode) so one-shot commands and the long-running
		// server never drift onto different exit conventions for the same
		// kind of error.
		os.Exit(exitcode.ExitCode(err))
	}
	if code, ok := cecdysis.ResultExitCode(leaf); ok {
		os.Exit(code)
	}
	os.Exit(exitcode.OK)
}

// wrapConfigParseError gives a malformed conduit.yaml a stable error code, the
// file path and a suggested fix. ecdysis surfaces viper's parse failure as a
// plain error carrying no code, so `--json` reported internal.unknown and the
// process exited 1 (runtime) instead of 2 (validation). Wrapping it at the
// single point every command's error passes through means a bad config file is
// classified as bad input on `run`, `generate`, `pipelines`, and every other
// command that loads the config.
//
// The path is cfg.ConduitCfg.Path, the default; a `--config.path` flag or
// CONDUIT_CONFIG_PATH env var may point elsewhere, and the yaml error already
// carries the line and column of the offending token.
func wrapConfigParseError(cfg conduit.Config, err error) error {
	var pe viper.ConfigParseError
	if !cerrors.As(err, &pe) {
		return err
	}
	ce := conduiterr.Wrap(
		config.CodeParseError,
		fmt.Sprintf("failed to parse conduit.yaml (%s): %s", cfg.ConduitCfg.Path, pe.Error()),
		pe,
	)
	ce.Suggestion = "fix the YAML syntax error at the reported line and column, then re-run"
	return ce
}
