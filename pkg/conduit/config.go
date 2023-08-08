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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/database"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/rs/zerolog"
)

const (
	DBTypeBadger   = "badger"
	DBTypePostgres = "postgres"
	DBTypeInMemory = "inmemory"
)

// Config holds all configurable values for Conduit.
type Config struct {
	DB struct {
		// When Driver is specified it takes precedence over other DB related
		// fields.
		Driver database.DB

		Type   string
		Badger struct {
			Path string
		}
		Postgres struct {
			ConnectionString string
			Table            string
		}
	}

	API struct {
		Disabled bool

		HTTP struct {
			Address string
		}
		GRPC struct {
			Address string
		}
	}

	Log struct {
		Level  string
		Format string
	}

	Connectors struct {
		Path string
	}

	Pipelines struct {
		Path        string
		ExitOnError bool
	}

	PluginDispenserFactories map[string]builtin.DispenserFactory
	ProcessorBuilderRegistry *processor.BuilderRegistry
}

func DefaultConfig() Config {
	var cfg Config
	cfg.DB.Type = "badger"
	cfg.DB.Badger.Path = "conduit.db"
	cfg.DB.Postgres.Table = "conduit_kv_store"
	cfg.API.HTTP.Address = ":8080"
	cfg.API.GRPC.Address = ":8084"
	cfg.Log.Level = "info"
	cfg.Log.Format = "cli"
	cfg.Connectors.Path = "./connectors"
	cfg.Pipelines.Path = "./pipelines"

	cfg.PluginDispenserFactories = builtin.DefaultDispenserFactories
	cfg.ProcessorBuilderRegistry = processor.GlobalBuilderRegistry
	return cfg
}

func (c Config) Validate() error {
	// TODO simplify validation with struct tags

	if c.DB.Driver == nil {
		switch c.DB.Type {
		case DBTypeBadger:
			if c.DB.Badger.Path == "" {
				return requiredConfigFieldErr("db.badger.path")
			}
		case DBTypePostgres:
			if c.DB.Postgres.ConnectionString == "" {
				return requiredConfigFieldErr("db.postgres.connection-string")
			}
			if c.DB.Postgres.Table == "" {
				return requiredConfigFieldErr("db.postgres.table")
			}
		case DBTypeInMemory:
			// all good
		default:
			return invalidConfigFieldErr("db.type")
		}
	}

	if !c.API.Disabled {
		if c.API.GRPC.Address == "" {
			return requiredConfigFieldErr("api.grpc.address")
		}
		if c.API.HTTP.Address == "" {
			return requiredConfigFieldErr("api.http.address")
		}
	}

	if c.Log.Level == "" {
		return requiredConfigFieldErr("log.level")
	}
	_, err := zerolog.ParseLevel(c.Log.Level)
	if err != nil {
		return invalidConfigFieldErr("log.level")
	}

	if c.Log.Format == "" {
		return requiredConfigFieldErr("log.format")
	}
	_, err = log.ParseFormat(c.Log.Format)
	if err != nil {
		return invalidConfigFieldErr("log.format")
	}

	if c.Pipelines.Path == "" {
		return requiredConfigFieldErr("pipelines.path")
	}
	// check if folder exists
	_, err = os.Stat(c.Pipelines.Path)
	if c.Pipelines.Path != "./pipelines" && os.IsNotExist(err) {
		return invalidConfigFieldErr("pipelines.path")
	}

	return nil
}

func invalidConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is invalid", name)
}

func requiredConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is required", name)
}
