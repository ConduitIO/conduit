// Copyright © 2022 Meroxa, Inc.
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

package conduit

import (
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/matryer/is"
)

// TestDefaultConfig_UIEnabledByDefault locks in the design doc's stated
// default (docs/design-documents/20260713-greenfield-built-in-ui.md §7:
// "observe on by default"): a fresh config serves the embedded UI unless an
// operator opts out.
func TestDefaultConfig_UIEnabledByDefault(t *testing.T) {
	is := is.New(t)
	cfg := DefaultConfigWithBasePath(t.TempDir())
	is.True(cfg.API.HTTP.UI.Enabled)
}

// TestDefaultConfig_AllowUnsignedDisabledByDefault locks in DeVaris's Tier-1
// posture decision: the --allow-unsigned escape-hatch CAPABILITY is off
// unless an operator explicitly opts in (install.allowUnsigned: true) — a
// fresh config must never permit an unsigned connector install by default,
// regardless of flag/TTY/env var (see pkg/registry/policy.Decide, which
// checks OperatorPolicy first and refuses unconditionally when it's false).
func TestDefaultConfig_AllowUnsignedDisabledByDefault(t *testing.T) {
	is := is.New(t)
	cfg := DefaultConfigWithBasePath(t.TempDir())
	is.True(!cfg.Install.AllowUnsigned)
}

func TestConfig_Validate(t *testing.T) {
	testCases := []struct {
		name        string
		setupConfig func(Config) Config
		want        error
	}{
		{
			name: "valid",
			setupConfig: func(c Config) Config {
				return c
			},
			want: nil,
		},
		{
			name: "valid CORS origins (exact, wildcard on loopback, port)",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.Address = "127.0.0.1:8080" // "*" is only valid on a loopback bind
				c.API.HTTP.CORS.AllowedOrigins = []string{"http://localhost:5173", "https://ui.example.com", "*"}
				return c
			},
			want: nil,
		},
		{
			name: "CORS wildcard refused on non-loopback bind",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.Address = "0.0.0.0:8080" // all interfaces
				c.API.HTTP.CORS.AllowedOrigins = []string{"*"}
				return c
			},
			want: invalidConfigFieldErr("api.http.cors.allowed-origins"),
		},
		{
			name: "CORS wildcard allowed on loopback bind",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.Address = "localhost:8080"
				c.API.HTTP.CORS.AllowedOrigins = []string{"*"}
				return c
			},
			want: nil,
		},
		{
			name: "exact CORS origins allowed on non-loopback bind",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.Address = "0.0.0.0:9090"
				c.API.HTTP.CORS.AllowedOrigins = []string{"https://ui.example.com"}
				return c
			},
			want: nil,
		},
		{
			name: "invalid CORS origin (trailing slash)",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.CORS.AllowedOrigins = []string{"http://localhost:5173/"}
				return c
			},
			want: invalidConfigFieldErr("api.http.cors.allowed-origins"),
		},
		{
			name: "invalid CORS origin (no scheme)",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.CORS.AllowedOrigins = []string{"localhost:5173"}
				return c
			},
			want: invalidConfigFieldErr("api.http.cors.allowed-origins"),
		},
		{
			name: "invalid CORS origin (has path)",
			setupConfig: func(c Config) Config {
				c.API.Enabled = true
				c.API.HTTP.CORS.AllowedOrigins = []string{"http://localhost:5173/app"}
				return c
			},
			want: invalidConfigFieldErr("api.http.cors.allowed-origins"),
		},
		{
			name: "invalid DB type (empty)",
			setupConfig: func(c Config) Config {
				c.DB.Type = ""
				return c
			},
			want: invalidConfigFieldErr("db.type"),
		},
		{
			name: "invalid DB type (invalid)",
			setupConfig: func(c Config) Config {
				c.DB.Type = "asdf"
				return c
			},
			want: invalidConfigFieldErr("db.type"),
		},
		{
			name: "required DB badger path",
			setupConfig: func(c Config) Config {
				c.DB.Type = DBTypeBadger
				c.DB.Badger.Path = ""
				return c
			},
			want: requiredConfigFieldErr("db.badger.path"),
		},
		{
			name: "required DB Postgres connection string",
			setupConfig: func(c Config) Config {
				c.DB.Type = DBTypePostgres
				c.DB.Postgres.ConnectionString = ""
				return c
			},
			want: requiredConfigFieldErr("db.postgres.connection-string"),
		},
		{
			name: "required DB Postgres table",
			setupConfig: func(c Config) Config {
				c.DB.Type = DBTypePostgres
				c.DB.Postgres.Table = ""
				return c
			},
			want: requiredConfigFieldErr("db.postgres.table"),
		},
		{
			name: "custom DB driver",
			setupConfig: func(c Config) Config {
				c.DB.Type = ""
				c.DB.Driver = &inmemory.DB{} // db driver explicitly defined
				return c
			},
			want: nil,
		},
		{
			name: "required HTTP address",
			setupConfig: func(c Config) Config {
				c.API.HTTP.Address = ""
				return c
			},
			want: requiredConfigFieldErr("api.http.address"),
		},
		{
			name: "required GRPC address",
			setupConfig: func(c Config) Config {
				c.API.GRPC.Address = ""
				return c
			},
			want: requiredConfigFieldErr("api.grpc.address"),
		},
		{
			name: "disabled API valid",
			setupConfig: func(c Config) Config {
				c.API.Enabled = false
				c.API.HTTP.Address = ""
				c.API.GRPC.Address = ""
				return c
			},
			want: nil,
		},
		{
			name: "invalid Log level (invalid)",
			setupConfig: func(c Config) Config {
				c.Log.Level = "who"
				return c
			},
			want: invalidConfigFieldErr("log.level"),
		},
		{
			name: "invalid Log format (invalid)",
			setupConfig: func(c Config) Config {
				c.Log.Format = "someFormat"
				return c
			},
			want: invalidConfigFieldErr("log.format"),
		},
		{
			name: "required Log level",
			setupConfig: func(c Config) Config {
				c.Log.Level = ""
				return c
			},
			want: requiredConfigFieldErr("log.level"),
		},
		{
			name: "required Log format",
			setupConfig: func(c Config) Config {
				c.Log.Format = ""
				return c
			},
			want: requiredConfigFieldErr("log.format"),
		},
		{
			name: "required pipelines path",
			setupConfig: func(c Config) Config {
				c.Pipelines.Path = ""
				return c
			},
			want: requiredConfigFieldErr("pipelines.path"),
		},
		{
			name: "invalid pipelines path",
			setupConfig: func(c Config) Config {
				c.Pipelines.Path = "folder-does-not-exist"
				return c
			},
			want: invalidConfigFieldErr("pipelines.path"),
		},
		{
			name: "error recovery: negative min-delay",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MinDelay = -time.Second
				return c
			},
			want: cerrors.New(`invalid error recovery config: "min-delay" config value must be positive (got: -1s)`),
		},
		{
			name: "error recovery: negative max-delay",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MaxDelay = -time.Second
				return c
			},
			want: cerrors.New(`invalid error recovery config: "max-delay" config value must be positive (got: -1s)`),
		},
		{
			name: "error recovery: min-delay greater than max-delay",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MinDelay = 2 * time.Second
				c.Pipelines.ErrorRecovery.MaxDelay = time.Second
				return c
			},
			want: cerrors.New(`invalid error recovery config: "min-delay" should be smaller than "max-delay"`),
		},
		{
			name: "error recovery: negative backoff-factor",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.BackoffFactor = -1
				return c
			},
			want: cerrors.New(`invalid error recovery config: "backoff-factor" config value mustn't be negative (got: -1)`),
		},
		{
			name: "error recovery: max-retries smaller than -1",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MaxRetries = lifecycle.InfiniteRetriesErrRecovery - 1
				return c
			},
			want: cerrors.New(`invalid error recovery config: invalid "max-retries" value. It must be -1 for infinite retries or >= 0`),
		},
		{
			name: "error recovery: with 0 max-retries ",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MaxRetries = 0
				return c
			},
			want: nil,
		},
		{
			name: "error recovery: negative max-retries-window",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MaxRetriesWindow = -time.Second
				return c
			},
			want: cerrors.New(`invalid error recovery config: "max-retries-window" config value must be positive (got: -1s)`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			validConfig := DefaultConfig()
			validConfig.DB.Postgres.ConnectionString = "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
			underTest := tc.setupConfig(validConfig)
			got := underTest.Validate()
			if tc.want == nil {
				is.NoErr(got)
			} else {
				is.True(got != nil) // expected an error
				is.Equal(tc.want.Error(), got.Error())
			}
		})
	}
}
