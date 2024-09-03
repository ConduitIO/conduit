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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/matryer/is"
)

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
			want: requiredConfigFieldErr("http.address"),
		},
		{
			name: "required GRPC address",
			setupConfig: func(c Config) Config {
				c.API.GRPC.Address = ""
				return c
			},
			want: requiredConfigFieldErr("grpc.address"),
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
			name: "error recovery: negative max-retries",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.MaxRetries = -1
				return c
			},
			want: cerrors.New(`invalid error recovery config: "max-retries" config value mustn't be negative (got: -1)`),
		},
		{
			name: "error recovery: negative healthy-after",
			setupConfig: func(c Config) Config {
				c.Pipelines.ErrorRecovery.HealthyAfter = -time.Second
				return c
			},
			want: cerrors.New(`invalid error recovery config: "healthy-after" config value must be positive (got: -1s)`),
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
