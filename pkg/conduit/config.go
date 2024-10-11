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
	"time"

	"github.com/conduitio/conduit-commons/database"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/rs/zerolog"
	"golang.org/x/exp/constraints"
)

const (
	DBTypeBadger   = "badger"
	DBTypePostgres = "postgres"
	DBTypeInMemory = "inmemory"
	DBTypeSQLite   = "sqlite"

	SchemaRegistryTypeConfluent = "confluent"
	SchemaRegistryTypeBuiltin   = "builtin"
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
		SQLite struct {
			Path  string
			Table string
		}
	}

	API struct {
		Enabled bool

		HTTP struct {
			Address string
		}
		GRPC struct {
			Address string
		}
	}

	Log struct {
		NewLogger func(level, format string) log.CtxLogger
		Level     string
		Format    string
	}

	Connectors struct {
		Path string
	}

	Processors struct {
		Path string
	}

	Pipelines struct {
		Path           string
		ExitOnDegraded bool
		ErrorRecovery  struct {
			// MinDelay is the minimum delay before restart: Default: 1 second
			MinDelay time.Duration
			// MaxDelay is the maximum delay before restart: Default: 10 minutes
			MaxDelay time.Duration
			// BackoffFactor is the factor by which the delay is multiplied after each restart: Default: 2
			BackoffFactor int
			// MaxRetries is the maximum number of restarts before the pipeline is considered unhealthy: Default: -1 (infinite)
			MaxRetries int64
			// MaxRetriesWindow is the duration window in which the max retries are counted: Default: 5 minutes
			MaxRetriesWindow time.Duration
		}
	}

	ConnectorPlugins map[string]sdk.Connector

	SchemaRegistry struct {
		Type string

		Confluent struct {
			ConnectionString string
		}
	}

	dev struct {
		cpuprofile   string
		memprofile   string
		blockprofile string
	}
}

func DefaultConfig() Config {
	var cfg Config

	cfg.DB.Type = DBTypeBadger
	cfg.DB.Badger.Path = "conduit.db"
	cfg.DB.Postgres.Table = "conduit_kv_store"
	cfg.DB.SQLite.Path = "conduit.db"
	cfg.DB.SQLite.Table = "conduit_kv_store"

	cfg.API.Enabled = true
	cfg.API.HTTP.Address = ":8080"
	cfg.API.GRPC.Address = ":8084"

	cfg.Log.NewLogger = newLogger
	cfg.Log.Level = "info"
	cfg.Log.Format = "cli"

	cfg.Connectors.Path = "./connectors"

	cfg.Processors.Path = "./processors"

	cfg.Pipelines.Path = "./pipelines"
	cfg.Pipelines.ErrorRecovery.MinDelay = time.Second
	cfg.Pipelines.ErrorRecovery.MaxDelay = 10 * time.Minute
	cfg.Pipelines.ErrorRecovery.BackoffFactor = 2
	cfg.Pipelines.ErrorRecovery.MaxRetries = lifecycle.InfiniteRetriesErrRecovery
	cfg.Pipelines.ErrorRecovery.MaxRetriesWindow = 5 * time.Minute

	cfg.SchemaRegistry.Type = SchemaRegistryTypeBuiltin

	cfg.ConnectorPlugins = builtin.DefaultBuiltinConnectors
	return cfg
}

func (c Config) validateDBConfig() error {
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
		case DBTypeSQLite:
			if c.DB.SQLite.Path == "" {
				return requiredConfigFieldErr("db.sqlite.path")
			}
			if c.DB.SQLite.Table == "" {
				return requiredConfigFieldErr("db.sqlite.table")
			}
		default:
			return invalidConfigFieldErr("db.type")
		}
	}
	return nil
}

func (c Config) validateSchemaRegistryConfig() error {
	switch c.SchemaRegistry.Type {
	case SchemaRegistryTypeConfluent:
		if c.SchemaRegistry.Confluent.ConnectionString == "" {
			return requiredConfigFieldErr("schema-registry.confluent.connection-string")
		}
	case SchemaRegistryTypeBuiltin:
		// all good
	default:
		return invalidConfigFieldErr("schema-registry.type")
	}
	return nil
}

func (c Config) validateErrorRecovery() error {
	errRecoveryCfg := c.Pipelines.ErrorRecovery
	var errs []error

	if err := requirePositiveValue("min-delay", errRecoveryCfg.MinDelay); err != nil {
		errs = append(errs, err)
	}
	if err := requirePositiveValue("max-delay", errRecoveryCfg.MaxDelay); err != nil {
		errs = append(errs, err)
	}
	if errRecoveryCfg.MaxDelay > 0 && errRecoveryCfg.MinDelay > errRecoveryCfg.MaxDelay {
		errs = append(errs, cerrors.New(`"min-delay" should be smaller than "max-delay"`))
	}
	if err := requireNonNegativeValue("backoff-factor", errRecoveryCfg.BackoffFactor); err != nil {
		errs = append(errs, err)
	}
	if errRecoveryCfg.MaxRetries < lifecycle.InfiniteRetriesErrRecovery {
		errs = append(errs, cerrors.Errorf(`invalid "max-retries" value. It must be %d for infinite retries or >= 0`, lifecycle.InfiniteRetriesErrRecovery))
	}
	if err := requirePositiveValue("max-retries-window", errRecoveryCfg.MaxRetriesWindow); err != nil {
		errs = append(errs, err)
	}

	return cerrors.Join(errs...)
}

func (c Config) Validate() error {
	// TODO simplify validation with struct tags

	if err := c.validateDBConfig(); err != nil {
		return err
	}

	if err := c.validateSchemaRegistryConfig(); err != nil {
		return err
	}

	if c.API.Enabled {
		if c.API.GRPC.Address == "" {
			return requiredConfigFieldErr("grpc.address")
		}
		if c.API.HTTP.Address == "" {
			return requiredConfigFieldErr("http.address")
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

	if err := c.validateErrorRecovery(); err != nil {
		return cerrors.Errorf("invalid error recovery config: %w", err)
	}
	return nil
}

func invalidConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is invalid", name)
}

func requiredConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is required", name)
}

func requireNonNegativeValue[T constraints.Integer](name string, value T) error {
	if value < 0 {
		return cerrors.Errorf("%q config value mustn't be negative (got: %v)", name, value)
	}

	return nil
}

func requirePositiveValue[T constraints.Integer](name string, value T) error {
	if value <= 0 {
		return cerrors.Errorf("%q config value must be positive (got: %v)", name, value)
	}

	return nil
}
