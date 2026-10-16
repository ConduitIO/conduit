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

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
	"github.com/spf13/viper"
)

// TestWrapConfigParseError checks that a malformed conduit.yaml surfaces with a
// stable code and classifies as a validation failure (exit 2), not an internal
// error (exit 1). It is the regression test for #2986.
func TestWrapConfigParseError(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "conduit.yaml")
	is.NoErr(os.WriteFile(cfgPath, []byte("pipelines:\n- id: [unclosed\n"), 0o600))

	v := viper.New()
	v.SetConfigFile(cfgPath)
	v.SetConfigType("yaml")
	rawErr := v.ReadInConfig()
	is.True(rawErr != nil)

	var pe viper.ConfigParseError
	is.True(errors.As(rawErr, &pe)) // the parser really returns a ConfigParseError

	cfg := conduit.DefaultConfig()
	cfg.ConduitCfg.Path = cfgPath

	wrapped := wrapConfigParseError(cfg, rawErr)
	is.True(wrapped != rawErr) // it was wrapped, not returned as-is

	var ce *conduiterr.ConduitError
	is.True(errors.As(wrapped, &ce))
	is.Equal(ce.Code, config.CodeParseError)
	is.True(ce.Suggestion != "")
	is.Equal(exitcode.ExitCode(wrapped), exitcode.Validation)
}

// TestWrapConfigParseError_Passthrough checks a non-parse error is returned
// unchanged, so error classification for every other failure path is untouched.
func TestWrapConfigParseError_Passthrough(t *testing.T) {
	is := is.New(t)
	err := errors.New("some other error")
	is.Equal(wrapConfigParseError(conduit.DefaultConfig(), err), err)
}
