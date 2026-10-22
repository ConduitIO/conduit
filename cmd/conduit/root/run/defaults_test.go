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

package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/cobra"
)

// runConfig drives the real `conduit run` command (flags, config file,
// environment) up to the point where it would start the engine, and returns the
// configuration it parsed. Execute is replaced so nothing starts.
func runConfig(t *testing.T, dir string, args ...string) conduit.Config {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	rc := &RunCommand{}
	cmd := ecdysis.New().MustBuildCobraCommand(rc)
	var got conduit.Config
	cmd.RunE = func(*cobra.Command, []string) error { got = rc.Cfg; return nil }
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return got
}

// The documented defaults of the file-access settings must hold on the real
// command line path, not only in conduit.DefaultConfig: with no flag, config
// file or environment set, `conduit run` confines the file connector to
// <dir>/files, TLS and credential files to <dir>/secrets and the system CA
// directories, and scripts to <dir>/scripts.
func TestRunDefaultsFileAccess(t *testing.T) {
	is := is.New(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := runConfig(t, dir)

	files, secrets, scripts := cfg.EffectiveAllowedDirs()
	is.Equal(files, []string{filepath.Join(dir, "files")})
	is.True(len(secrets) > 1 && secrets[0] == filepath.Join(dir, "secrets"))
	is.Equal(scripts, []string{filepath.Join(dir, "scripts")})
	for _, s := range secrets {
		is.True(s != "/run/secrets") // only explicitly configured names, never the whole directory
	}
}

func TestRunExplicitSettingsReplaceDefaults(t *testing.T) {
	is := is.New(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := runConfig(t, dir,
		"--connectors.file.allowed-dirs", "/data/a",
		"--connectors.file.allowed-dirs", "/data/b",
		"--secrets.allowed-dirs", "/etc/conduit/tls",
		"--processors.javascript.allowed-dirs", "/opt/js",
	)
	files, secrets, scripts := cfg.EffectiveAllowedDirs()
	is.Equal(files, []string{"/data/a", "/data/b"})
	is.Equal(secrets, []string{"/etc/conduit/tls"})
	is.Equal(scripts, []string{"/opt/js"})
}

func TestRunDefaultsFromEnvAndConfigFile(t *testing.T) {
	is := is.New(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())

	t.Setenv("CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS", "/from/env")
	cfg := runConfig(t, dir)
	files, secrets, _ := cfg.EffectiveAllowedDirs()
	is.Equal(files, []string{"/from/env"})
	// secrets still default: one explicit setting does not disable the others
	is.True(len(secrets) > 1 && secrets[0] == filepath.Join(dir, "secrets"))

	t.Setenv("CONDUIT_CONNECTORS_FILE_ALLOWED_DIRS", "")
	cfgFile := filepath.Join(dir, "conduit.yaml")
	if err := os.WriteFile(cfgFile, []byte("connectors:\n  file:\n    allowed-dirs:\n      - /from/yaml\nsecrets:\n  allowed-dirs:\n    - /yaml/secrets\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = runConfig(t, dir, "--config.path", cfgFile)
	files, secrets, scripts := cfg.EffectiveAllowedDirs()
	is.Equal(files, []string{"/from/yaml"})
	is.Equal(secrets, []string{"/yaml/secrets"})
	is.Equal(scripts, []string{filepath.Join(dir, "scripts")})
}
