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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/pocketbase/pocketbase"
	"github.com/rs/zerolog"
)

const (
	DBTypeBadger     = "badger"
	DBTypePostgres   = "postgres"
	DBTypeInMemory   = "inmemory"
	DBTypePocketbase = "pocketbase"
)

// Config holds all configurable values for Conduit.
type Config struct {
	DB struct {
		Type   string
		Badger struct {
			Path string
		}
		Postgres struct {
			ConnectionString string
			Table            string
		}
		PocketBase struct {
			PocketBase *pocketbase.PocketBase
			Table      string
		}
	}

	HTTP struct {
		Address  string
		Disabled bool
	}
	GRPC struct {
		Address  string
		Disabled bool
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
}

//nolint:gocyclo // this will be reworked with struct tags at some points
func (c Config) Validate() error {
	// TODO simplify validation with struct tags

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
	case DBTypePocketbase:
		// all good
		if c.DB.PocketBase.PocketBase == nil {
			return requiredConfigFieldErr("db.pocketbase.pocketbase")
		}
		if c.DB.PocketBase.Table == "" {
			return requiredConfigFieldErr("db.pocketbase.table")
		}
	default:
		return invalidConfigFieldErr("db.type")
	}

	if !c.GRPC.Disabled {
		if c.GRPC.Address == "" {
			return requiredConfigFieldErr("grpc.address")
		}
	}

	if !c.GRPC.Disabled && !c.HTTP.Disabled {
		if c.HTTP.Address == "" {
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

	return nil
}

func invalidConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is invalid", name)
}

func requiredConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is required", name)
}
