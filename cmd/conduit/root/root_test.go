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

package root

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
	isT "github.com/matryer/is"
)

func TestRootCommandFlags(t *testing.T) {
	is := isT.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		required   bool
		persistent bool
		hidden     bool
	}{
		{longName: "config.path", persistent: true},
		{longName: "version", shortName: "v", persistent: true},
		{longName: "db.type"},
		{longName: "db.badger.path"},
		{longName: "db.postgres.connection-string"},
		{longName: "db.postgres.table"},
		{longName: "db.sqlite.path"},
		{longName: "db.sqlite.table"},
		{longName: "api.enabled"},
		{longName: "http.address"},
		{longName: "grpc.address"},
		{longName: "log.level"},
		{longName: "log.format"},
		{longName: "connectors.path"},
		{longName: "processors.path"},
		{longName: "pipelines.path"},
		{longName: "pipelines.exit-on-degraded"},
		{longName: "pipelines.error-recovery.min-delay"},
		{longName: "pipelines.error-recovery.max-delay"},
		{longName: "pipelines.error-recovery.backoff-factor"},
		{longName: "pipelines.error-recovery.max-retries"},
		{longName: "pipelines.error-recovery.max-retries-window"},
		{longName: "schema-registry.type"},
		{longName: "schema-registry.confluent.connection-string"},
		{longName: "preview.pipeline-arch-v2"},
		{longName: "dev.cpuprofile"},
		{longName: "dev.memprofile"},
		{longName: "dev.blockprofile"},
	}

	c := &RootCommand{}
	flags := c.Flags()

	for _, ef := range expectedFlags {
		var foundFlag *ecdysis.Flag
		for _, f := range flags {
			if f.Long == ef.longName {
				foundFlag = &f
				break
			}
		}

		is.True(foundFlag != nil)

		if foundFlag != nil {
			is.Equal(ef.shortName, foundFlag.Short)
			is.Equal(ef.required, foundFlag.Required)
			is.Equal(ef.persistent, foundFlag.Persistent)
			is.Equal(ef.hidden, foundFlag.Hidden)
		}
	}
}

func TestRootCommand_updateConfig(t *testing.T) {
	is := isT.New(t)

	tmpDir, err := os.MkdirTemp("", "config-test")
	is.NoErr(err)
	defer os.RemoveAll(tmpDir)

	configFileContent := `
db:
  type: "sqlite"
  sqlite:
    path: "/custom/path/db"
log:
  level: "debug"
  format: "json"
api:
  enabled: false
`

	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(configFileContent), 0644)
	is.NoErr(err)

	tests := []struct {
		name       string
		flags      *RootFlags
		configFile string
		envVars    map[string]string
		assertFunc func(*isT.I, conduit.Config)
	}{
		{
			name: "default values only",
			flags: &RootFlags{
				ConduitConfigPath: "nonexistent.yaml",
			},
			assertFunc: func(is *isT.I, cfg conduit.Config) {
				defaultCfg := conduit.DefaultConfigWithBasePath(tmpDir)
				is.Equal(cfg.DB.Type, defaultCfg.DB.Type)
				is.Equal(cfg.Log.Level, defaultCfg.Log.Level)
				is.Equal(cfg.API.Enabled, defaultCfg.API.Enabled)
			},
		},
		{
			name: "config file overrides defaults",
			flags: &RootFlags{
				ConduitConfigPath: configPath,
			},
			assertFunc: func(is *isT.I, cfg conduit.Config) {
				is.Equal(cfg.DB.Type, "sqlite")
				is.Equal(cfg.DB.SQLite.Path, "/custom/path/db")
				is.Equal(cfg.Log.Level, "debug")
				is.Equal(cfg.Log.Format, "json")
				is.Equal(cfg.API.Enabled, false)
			},
		},
		{
			name: "env vars override config file",
			flags: &RootFlags{
				ConduitConfigPath: configPath,
			},
			envVars: map[string]string{
				"CONDUIT_DB_TYPE":     "postgres",
				"CONDUIT_LOG_LEVEL":   "warn",
				"CONDUIT_API_ENABLED": "true",
			},
			assertFunc: func(is *isT.I, cfg conduit.Config) {
				is.Equal(cfg.DB.Type, "postgres")
				is.Equal(cfg.Log.Level, "warn")
				is.Equal(cfg.API.Enabled, true)
				// Config file values that weren't overridden should remain
				is.Equal(cfg.Log.Format, "json")
			},
		},
		{
			name: "flags override everything",
			flags: &RootFlags{
				ConduitConfigPath: configPath,
				Config: conduit.Config{
					DB:  conduit.Config{}.DB,
					Log: conduit.Config{}.Log,
					API: conduit.Config{}.API,
				},
			},
			envVars: map[string]string{
				"CONDUIT_DB_TYPE":     "postgres",
				"CONDUIT_LOG_LEVEL":   "warn",
				"CONDUIT_API_ENABLED": "false",
			},
			assertFunc: func(is *isT.I, cfg conduit.Config) {
				is.Equal(cfg.DB.Type, "badger")
				is.Equal(cfg.Log.Level, "error")
				is.Equal(cfg.Log.Format, "text")
				is.Equal(cfg.API.Enabled, true)
			},
		},
		{
			name: "partial overrides",
			flags: &RootFlags{
				ConduitConfigPath: configPath,
				Config: conduit.Config{
					Log: conduit.Config{}.Log,
				},
			},
			envVars: map[string]string{
				"CONDUIT_DB_TYPE": "postgres",
			},
			assertFunc: func(is *isT.I, cfg conduit.Config) {
				is.Equal(cfg.DB.Type, "postgres")
				is.Equal(cfg.Log.Level, "error")
				is.Equal(cfg.Log.Format, "json")
				is.Equal(cfg.API.Enabled, false)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := isT.New(t)

			os.Clearenv()

			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.envVars {
					os.Unsetenv(k)
				}
			}()

			c := &RootCommand{
				flags: *tt.flags,
			}

			err := c.updateConfig()
			is.NoErr(err)

			tt.assertFunc(is, c.cfg)
		})
	}
}
