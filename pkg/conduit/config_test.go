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

	"github.com/conduitio/conduit/pkg/foundation/assert"
)

func TestConfig_Validate(t *testing.T) {
	testCases := []struct {
		name        string
		setupConfig func(Config) Config
		want        error
	}{{
		name: "valid",
		setupConfig: func(c Config) Config {
			return c
		},
		want: nil,
	}, {
		name: "invalid DB type (empty)",
		setupConfig: func(c Config) Config {
			c.DB.Type = ""
			return c
		},
		want: invalidConfigFieldErr("db.type"),
	}, {
		name: "invalid DB type (invalid)",
		setupConfig: func(c Config) Config {
			c.DB.Type = "asdf"
			return c
		},
		want: invalidConfigFieldErr("db.type"),
	}, {
		name: "required DB badger path",
		setupConfig: func(c Config) Config {
			c.DB.Type = DBTypeBadger
			c.DB.Badger.Path = ""
			return c
		},
		want: requiredConfigFieldErr("db.badger.path"),
	}, {
		name: "required DB Postgres connection string",
		setupConfig: func(c Config) Config {
			c.DB.Type = DBTypePostgres
			c.DB.Postgres.ConnectionString = ""
			return c
		},
		want: requiredConfigFieldErr("db.postgres.connection-string"),
	}, {
		name: "required DB Postgres table",
		setupConfig: func(c Config) Config {
			c.DB.Type = DBTypePostgres
			c.DB.Postgres.Table = ""
			return c
		},
		want: requiredConfigFieldErr("db.postgres.table"),
	}, {
		name: "required HTTP address",
		setupConfig: func(c Config) Config {
			c.HTTP.Address = ""
			return c
		},
		want: requiredConfigFieldErr("http.address"),
	}, {
		name: "required GRPC address",
		setupConfig: func(c Config) Config {
			c.GRPC.Address = ""
			return c
		},
		want: requiredConfigFieldErr("grpc.address"),
	}, {
		name: "invalid Log level (invalid)",
		setupConfig: func(c Config) Config {
			c.Log.Level = "who"
			return c
		},
		want: invalidConfigFieldErr("log.level"),
	}, {
		name: "invalid Log format (invalid)",
		setupConfig: func(c Config) Config {
			c.Log.Format = "someFormat"
			return c
		},
		want: invalidConfigFieldErr("log.format"),
	}, {
		name: "required Log level",
		setupConfig: func(c Config) Config {
			c.Log.Level = ""
			return c
		},
		want: requiredConfigFieldErr("log.level"),
	}, {
		name: "required Log format",
		setupConfig: func(c Config) Config {
			c.Log.Format = ""
			return c
		},
		want: requiredConfigFieldErr("log.format"),
	}, {
		name: "required pipelines path",
		setupConfig: func(c Config) Config {
			c.Pipelines.Path = ""
			return c
		},
		want: requiredConfigFieldErr("pipelines.path"),
	}, {
		name: "invalid pipelines path",
		setupConfig: func(c Config) Config {
			c.Pipelines.Path = "folder-does-not-exist"
			return c
		},
		want: invalidConfigFieldErr("pipelines.path"),
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var validConfig Config
			validConfig.DB.Type = DBTypeInMemory
			validConfig.DB.Badger.Path = "conduit.app"
			validConfig.DB.Postgres.Table = "conduit_kv_store"
			validConfig.DB.Postgres.ConnectionString = "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
			validConfig.HTTP.Address = ":8080"
			validConfig.GRPC.Address = ":8084"
			validConfig.Log.Level = "info"
			validConfig.Log.Format = "cli"
			validConfig.Pipelines.Path = "./pipelines"

			underTest := tc.setupConfig(validConfig)
			got := underTest.Validate()
			if got == nil {
				assert.Nil(t, tc.want)
			} else {
				assert.Equal(t, tc.want.Error(), got.Error())
			}
		})
	}
}
