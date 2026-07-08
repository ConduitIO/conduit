// Copyright © 2026 Meroxa, Inc.
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

package doctorcheck

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// configResolveCheck reports whether cfg's config file was found on disk.
//
// By the time doctor's checks run, cfg is already the fully merged
// configuration (file + env + flags + defaults) — the CLI command resolves
// it via the same ecdysis config-parsing pipeline `conduit run` uses,
// before ExecuteWithResult (and therefore this check) ever runs. So this
// check cannot observe an "unparseable config file" outcome: a malformed
// YAML file fails config parsing before doctor's check set is even built,
// surfacing as a HARD command failure instead (see
// cmd/conduit/root/doctor's doc for that limitation). What this check adds
// on top of that is a simple, direct signal for the common two remaining
// cases: a config file was found and used, or none was found and defaults
// apply — useful on its own since ecdysis's own config resolution treats a
// missing file as a silent, unsurfaced non-error.
type configResolveCheck struct {
	cfg conduit.Config
}

func (c configResolveCheck) Name() string { return NameConfigResolve }

func (c configResolveCheck) Run(_ context.Context) check.CheckResult {
	path := c.cfg.ConduitCfg.Path

	info, err := os.Stat(path)
	switch {
	case err == nil && !info.IsDir():
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryConfig,
			ConfigPath: configKeyConfigPath,
			Message:    fmt.Sprintf("config file found at %s", path),
		}
	case err == nil && info.IsDir():
		return check.CheckResult{
			Status:     check.StatusFail,
			Category:   CategoryConfig,
			Code:       conduiterr.CodeInvalidArgument.Reason(),
			ConfigPath: configKeyConfigPath,
			Message:    fmt.Sprintf("%s is a directory, not a config file", path),
			Suggestion: "point config.path at a file, or remove the directory",
		}
	case os.IsNotExist(err):
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryConfig,
			ConfigPath: configKeyConfigPath,
			Message:    fmt.Sprintf("no config file at %s — using built-in defaults and any flags/env vars", path),
		}
	default:
		// Some other stat error (permission denied, a bad path component,
		// etc.) — distinct from "no file here at all", so don't claim that
		// instead. Still a Warn, not a Fail: by the time this check runs,
		// ecdysis's own config resolution already tolerated whatever this
		// is (it only hard-fails PreRunE on a genuinely unparseable file
		// that WAS read — see this type's doc), so treat it as a
		// lower-confidence version of the same "couldn't confirm a config
		// file was used" signal rather than escalating past what upstream
		// already decided.
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryConfig,
			ConfigPath: configKeyConfigPath,
			Message:    fmt.Sprintf("could not check for a config file at %s: %v", path, err),
		}
	}
}

// configValidateCheck reuses conduit.Config.Validate() — the exact call
// conduit.NewRuntime makes before starting anything — so doctor never
// drifts from what `conduit run` actually enforces.
type configValidateCheck struct {
	cfg conduit.Config
}

func (c configValidateCheck) Name() string { return NameConfigValidate }

func (c configValidateCheck) Run(_ context.Context) check.CheckResult {
	err := c.cfg.Validate()
	if err == nil {
		return check.CheckResult{
			Status:   check.StatusPass,
			Category: CategoryConfig,
			Message:  "configuration is valid",
		}
	}

	return check.CheckResult{
		Status:     check.StatusFail,
		Category:   CategoryConfig,
		Code:       conduiterr.CodeInvalidArgument.Reason(),
		ConfigPath: configPathFromValidateErr(err),
		Message:    err.Error(),
		Suggestion: "fix the invalid or missing config field and re-run `conduit doctor`",
	}
}

// configFieldPattern extracts the dotted config key from a
// Config.Validate() error message. Every leaf error Validate can return
// today is produced by requiredConfigFieldErr or invalidConfigFieldErr
// (pkg/conduit/config.go), both of which start the message with the
// quoted, dotted field name (e.g. `"db.badger.path" config value is
// required`) — this pattern captures exactly that quoted prefix.
//
// It deliberately does not attempt to parse the error-recovery validation
// errors (validateErrorRecovery), which join multiple, differently-shaped
// messages under an "invalid error recovery config: %w" wrapper: a
// best-effort match there would silently anchor a fail result to a
// misleading path. When the pattern doesn't match, ConfigPath is left
// empty rather than guessed at — a --json consumer or a human still gets
// the fail with its full message, they just don't get a configPath
// pointer.
var configFieldPattern = regexp.MustCompile(`^"([a-zA-Z0-9_.-]+)" config value`)

func configPathFromValidateErr(err error) string {
	m := configFieldPattern.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return ""
	}
	return m[1]
}
